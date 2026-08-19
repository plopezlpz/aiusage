import AppKit
import Combine
import Darwin
import Foundation
import SwiftUI

struct DashboardSnapshot: Decodable, Equatable {
    let version: Int
    let generatedAt: Int64
    let state: String
    let message: String
    let quotas: [DashboardQuota]

    static func decode(_ data: Data, now: Date = Date()) throws -> DashboardSnapshot {
        let snapshot = try JSONDecoder().decode(Self.self, from: data)
        try snapshot.validate(now: now)
        return snapshot
    }

    private func validate(now: Date) throws {
        guard version == 1 else { throw DashboardDataError.invalid("Unsupported dashboard data version.") }
        guard ["ready", "partial", "unavailable"].contains(state) else {
            throw DashboardDataError.invalid("Invalid dashboard state.")
        }
        let earliest = Int64(DateComponents(calendar: Calendar(identifier: .gregorian), timeZone: .gmt, year: 2020, month: 1, day: 1).date!.timeIntervalSince1970)
        let earliestGenerated = Int64(now.addingTimeInterval(-300).timeIntervalSince1970)
        let latestGenerated = Int64(now.addingTimeInterval(300).timeIntervalSince1970)
        guard generatedAt >= earliestGenerated, generatedAt <= latestGenerated else {
            throw DashboardDataError.invalid("Invalid generated timestamp.")
        }
        var ids = Set<String>()
        for quota in quotas {
            guard !quota.id.isEmpty, ids.insert(quota.id).inserted else {
                throw DashboardDataError.invalid("Duplicate or empty quota identifier.")
            }
            guard !quota.provider.isEmpty, !quota.product.isEmpty, !quota.window.isEmpty else {
                throw DashboardDataError.invalid("Quota labels cannot be empty.")
            }
            guard quota.remainingPercent.isFinite, (0...100).contains(quota.remainingPercent) else {
                throw DashboardDataError.invalid("Remaining percentage is outside 0–100.")
            }
            for timestamp in [quota.updatedAt, quota.attemptedAt].compactMap({ $0 }) {
                guard timestamp >= earliest, timestamp <= generatedAt + 300 else {
                    throw DashboardDataError.invalid("Invalid quota timestamp.")
                }
            }
            if let reset = quota.resetAt {
                guard reset >= earliest, reset <= generatedAt + 8 * 24 * 60 * 60 else {
                    throw DashboardDataError.invalid("Invalid quota reset timestamp.")
                }
            }
        }
    }
}

struct DashboardQuota: Decodable, Equatable, Identifiable {
    let id: String
    let provider: String
    let product: String
    let window: String
    let remainingPercent: Double
    let resetAt: Int64?
    let updatedAt: Int64?
    let attemptedAt: Int64?
    let failure: String
    let source: String
    let detail: String
    let stale: Bool
}

struct QuotaGroup: Equatable, Identifiable {
    let provider: String
    let product: String
    var quotas: [DashboardQuota]
    var id: String { provider + "\u{0}" + product }

    static func stableGroups(_ quotas: [DashboardQuota]) -> [QuotaGroup] {
        var groups: [QuotaGroup] = []
        for quota in quotas {
            if let index = groups.firstIndex(where: { $0.provider == quota.provider && $0.product == quota.product }) {
                groups[index].quotas.append(quota)
            } else {
                groups.append(QuotaGroup(provider: quota.provider, product: quota.product, quotas: [quota]))
            }
        }
        return groups
    }
}

enum DashboardDataError: LocalizedError {
    case invalid(String)
    var errorDescription: String? {
        switch self { case .invalid(let message): return message }
    }
}

enum DashboardRefresh: Int {
    case cache
    case auto
    case force

    var arguments: [String] {
        switch self {
        case .cache: return ["dashboard-json"]
        case .auto: return ["dashboard-json", "--refresh=auto"]
        case .force: return ["dashboard-json", "--refresh=force"]
        }
    }
}

func queuedDashboardRefresh(active: DashboardRefresh, pending: DashboardRefresh?, incoming: DashboardRefresh, resetFired: Bool = false) -> DashboardRefresh? {
    if resetFired { return pending == .force ? pending : .auto }
    return incoming.rawValue > active.rawValue && incoming.rawValue > (pending?.rawValue ?? -1) ? incoming : pending
}

func preferredResetRefreshDeadline(current: TimeInterval?, resetDates: [Int64], now: TimeInterval) -> TimeInterval? {
    let candidate = resetDates.map { TimeInterval($0) + 5 }.filter { $0 > now }.min()
    guard let current else { return candidate }
    guard let candidate else { return current }
    return min(current, candidate)
}

struct DarwinProcessIdentity: Equatable, Hashable, Sendable {
    let pid: pid_t
    let startSeconds: UInt64
    let startMicroseconds: UInt64
}

func processIdentityMatches(_ expected: DarwinProcessIdentity, _ current: DarwinProcessIdentity?) -> Bool {
    current == expected
}

func descendantDepth(pid: pid_t, ancestor: pid_t, parentOf: (pid_t) -> pid_t?) -> Int? {
    guard pid > 1, ancestor > 1, pid != ancestor else { return nil }
    var current = pid
    var depth = 0
    var visited = Set<pid_t>()
    while current > 1, visited.insert(current).inserted {
        guard let parent = parentOf(current), parent > 0 else { return nil }
        depth += 1
        if parent == ancestor { return depth }
        current = parent
    }
    return nil
}

private struct DarwinProcessInfo {
    let identity: DarwinProcessIdentity
    let parentPID: pid_t
}

private func darwinProcessInfo(_ pid: pid_t) -> DarwinProcessInfo? {
    guard pid > 1 else { return nil }
    var info = proc_bsdinfo()
    let expected = Int32(MemoryLayout<proc_bsdinfo>.size)
    let received = withUnsafeMutablePointer(to: &info) {
        proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, $0, expected)
    }
    guard received == expected else { return nil }
    return DarwinProcessInfo(
        identity: DarwinProcessIdentity(
            pid: pid,
            startSeconds: info.pbi_start_tvsec,
            startMicroseconds: info.pbi_start_tvusec
        ),
        parentPID: pid_t(info.pbi_ppid)
    )
}

private func darwinProcessIdentity(_ pid: pid_t) -> DarwinProcessIdentity? {
    darwinProcessInfo(pid)?.identity
}

private func darwinParentPID(_ pid: pid_t) -> pid_t? {
    darwinProcessInfo(pid)?.parentPID
}

@MainActor
final class DashboardStore: ObservableObject {
    @Published private(set) var snapshot: DashboardSnapshot?
    @Published private(set) var isRefreshing = false
    @Published private(set) var refreshKind: DashboardRefresh?
    @Published private(set) var transportError: String?

    var groups: [QuotaGroup] { QuotaGroup.stableGroups(snapshot?.quotas ?? []) }

    private let executable: URL
    private let worker = DispatchQueue(label: "com.plopezlpz.aiusage.dashboard-process", qos: .userInitiated)
    private nonisolated let lifecycle = HelperLifecycle()
    private var stopping = false
    private var pendingRefresh: DashboardRefresh?
    private var cacheTimer: Timer?
    private var autoTimer: Timer?
    private var resetTimer: Timer?
    private var resetDeadline: TimeInterval?

    init(executable: URL) {
        self.executable = executable
    }

    func start() {
        request(.cache)
        request(.auto)
        cacheTimer = .scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.request(.cache) }
        }
        autoTimer = .scheduledTimer(withTimeInterval: 300, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.request(.auto) }
        }
    }

    func request(_ kind: DashboardRefresh) {
        request(kind, resetFired: false)
    }

    private func request(_ kind: DashboardRefresh, resetFired: Bool) {
        guard !stopping else { return }
        if let active = refreshKind {
            pendingRefresh = queuedDashboardRefresh(active: active, pending: pendingRefresh, incoming: kind, resetFired: resetFired)
            return
        }
        isRefreshing = true
        refreshKind = kind
        let executable = self.executable
        let lifecycle = self.lifecycle
        worker.async { [weak self] in
            let result = Self.runHelper(kind, executable: executable, lifecycle: lifecycle)
            DispatchQueue.main.async { self?.finish(kind, result: result) }
        }
    }

    func stop(completion: @escaping () -> Void) {
        stopping = true
        cacheTimer?.invalidate()
        autoTimer?.invalidate()
        resetTimer?.invalidate()
        resetTimer = nil
        resetDeadline = nil
        pendingRefresh = nil
        guard let target = lifecycle.beginStop() else {
            completion()
            return
        }
        pollTermination(target: target, descendants: [:], deadline: Date().addingTimeInterval(8), completion: completion)
    }

    private func finish(_ kind: DashboardRefresh, result: Result<DashboardSnapshot, Error>) {
        guard refreshKind == kind else { return }
        switch result {
        case .success(let value):
            snapshot = value
            transportError = nil
            scheduleResetRefresh(for: value)
        case .failure(let error):
            if !stopping { transportError = error.localizedDescription }
        }
        isRefreshing = false
        refreshKind = nil
        if let next = pendingRefresh, !stopping {
            pendingRefresh = nil
            request(next)
        }
    }

    private func scheduleResetRefresh(for snapshot: DashboardSnapshot) {
        let now = Date().timeIntervalSince1970
        let current = resetTimer?.isValid == true ? resetDeadline : nil
        guard let deadline = preferredResetRefreshDeadline(
            current: current,
            resetDates: snapshot.quotas.compactMap(\.resetAt),
            now: now
        ) else {
            resetTimer?.invalidate()
            resetTimer = nil
            resetDeadline = nil
            return
        }
        guard deadline != current else { return }

        resetTimer?.invalidate()
        resetDeadline = deadline
        resetTimer = .scheduledTimer(withTimeInterval: deadline - now, repeats: false) { [weak self] _ in
            Task { @MainActor in self?.resetRefreshFired(deadline: deadline) }
        }
    }

    private func resetRefreshFired(deadline: TimeInterval) {
        guard resetDeadline == deadline else { return }
        resetTimer = nil
        resetDeadline = nil
        request(.auto, resetFired: true)
    }

    private func pollTermination(
        target original: ActiveHelper,
        descendants existing: [pid_t: DescendantTarget],
        deadline: Date,
        completion: @escaping () -> Void
    ) {
        let terminationState = lifecycle.terminationState
        let active = terminationState.target ?? original
        var descendants = existing
        if Self.isLive(active) {
            for target in Self.observedDescendants(of: active) {
                descendants[target.identity.pid] = target
            }
        }
        let remaining = Self.verifiedTargets(Array(descendants.values))
        if Date() >= deadline {
            DispatchQueue.global(qos: .userInitiated).async {
                Self.forceStop(active, retainedDescendants: Array(descendants.values))
                DispatchQueue.main.async(execute: completion)
            }
            return
        }
        if terminationState.isPrepared {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { [weak self] in
                self?.pollTermination(target: active, descendants: descendants, deadline: deadline, completion: completion)
            }
            return
        }
        guard Self.isLive(active) || !remaining.isEmpty else {
            completion()
            return
        }
        if Self.isLive(active) {
            active.process.terminate()
        } else {
            Self.signalVerified(remaining, signal: SIGTERM)
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { [weak self] in
            self?.pollTermination(target: active, descendants: descendants, deadline: deadline, completion: completion)
        }
    }

    private nonisolated static func forceStop(_ active: ActiveHelper, retainedDescendants: [DescendantTarget]) {
        guard isLive(active) else {
            signalVerified(retainedDescendants, signal: SIGKILL)
            return
        }
        guard !isSessionLeader(active) else {
            killActiveSession(active)
            signalVerified(retainedDescendants, signal: SIGKILL)
            return
        }
        killActiveTree(active, retainedDescendants: retainedDescendants)
    }

    private nonisolated static func isLive(_ active: ActiveHelper) -> Bool {
        guard active.pid > 1,
              active.process.processIdentifier == active.pid,
              active.process.isRunning else { return false }
        guard let identity = active.identity else { return true }
        return processIdentityMatches(identity, darwinProcessIdentity(active.pid))
    }

    private nonisolated static func isSessionLeader(_ active: ActiveHelper) -> Bool {
        isLive(active) && getsid(active.pid) == active.pid
    }

    private nonisolated static func observedDescendants(of active: ActiveHelper) -> [DescendantTarget] {
        guard isLive(active) else { return [] }
        return allPIDs().compactMap { candidate in
            guard let depth = descendantDepth(pid: candidate, ancestor: active.pid, parentOf: darwinParentPID),
                  let identity = darwinProcessIdentity(candidate),
                  isLive(active),
                  descendantDepth(pid: candidate, ancestor: active.pid, parentOf: darwinParentPID) != nil,
                  processIdentityMatches(identity, darwinProcessIdentity(candidate)) else { return nil }
            return DescendantTarget(identity: identity, depth: depth)
        }
    }

    private nonisolated static func verifiedTargets(_ targets: [DescendantTarget]) -> [DescendantTarget] {
        targets.filter { processIdentityMatches($0.identity, darwinProcessIdentity($0.identity.pid)) }
    }

    private nonisolated static func signalVerified(_ targets: [DescendantTarget], signal: Int32) {
        for target in verifiedTargets(targets).sorted(by: {
            $0.depth == $1.depth ? $0.identity.pid > $1.identity.pid : $0.depth > $1.depth
        }) {
            guard processIdentityMatches(target.identity, darwinProcessIdentity(target.identity.pid)) else { continue }
            kill(target.identity.pid, signal)
        }
    }

    private nonisolated static func killActiveTree(_ active: ActiveHelper, retainedDescendants: [DescendantTarget]) {
        guard isLive(active), kill(active.pid, SIGSTOP) == 0, isLive(active) else {
            killActivePID(active)
            signalVerified(retainedDescendants, signal: SIGKILL)
            return
        }

        var descendants = Dictionary(uniqueKeysWithValues: retainedDescendants.map { ($0.identity.pid, $0) })
        for pass in 0..<4 {
            guard isLive(active) else {
                signalVerified(Array(descendants.values), signal: SIGKILL)
                return
            }
            for target in observedDescendants(of: active) {
                descendants[target.identity.pid] = target
            }
            signalVerified(Array(descendants.values), signal: SIGKILL)
            if pass < 3 { usleep(50_000) }
        }
        killActivePID(active)
        signalVerified(Array(descendants.values), signal: SIGKILL)
    }

    private nonisolated static func killActiveSession(_ active: ActiveHelper) {
        guard isSessionLeader(active), kill(active.pid, SIGSTOP) == 0, isSessionLeader(active) else {
            killActivePID(active)
            return
        }

        for pass in 0..<4 {
            guard isSessionLeader(active) else {
                killActivePID(active)
                return
            }
            for candidate in allPIDs() where candidate > 1 && candidate != active.pid {
                guard isSessionLeader(active) else {
                    killActivePID(active)
                    return
                }
                guard getsid(candidate) == active.pid,
                      isSessionLeader(active),
                      getsid(candidate) == active.pid else { continue }
                kill(candidate, SIGKILL)
            }
            if pass < 3 { usleep(50_000) }
        }

        killSessionLeader(active)
    }

    private nonisolated static func killSessionLeader(_ active: ActiveHelper) {
        guard isSessionLeader(active) else {
            killActivePID(active)
            return
        }
        kill(active.pid, SIGKILL)
        waitForExit(active)
    }

    private nonisolated static func killActivePID(_ active: ActiveHelper) {
        guard isLive(active) else { return }
        kill(active.pid, SIGKILL)
        waitForExit(active)
    }

    private nonisolated static func waitForExit(_ active: ActiveHelper) {
        for _ in 0..<20 where active.process.isRunning { usleep(10_000) }
    }

    private nonisolated static func allPIDs() -> [pid_t] {
        let maximumCapacity = 131_072
        let reported = max(Int(proc_listallpids(nil, 0)), 0)
        var capacity = min(max(reported + max(reported / 4, 32), 64), maximumCapacity)

        while true {
            var pids = [pid_t](repeating: 0, count: capacity)
            let count = pids.withUnsafeMutableBytes {
                proc_listallpids($0.baseAddress, Int32($0.count))
            }
            guard count > 0 else { return [] }
            if Int(count) < capacity || capacity == maximumCapacity {
                return Array(pids.prefix(min(Int(count), pids.count)))
            }
            capacity = min(capacity * 2, maximumCapacity)
        }
    }

    nonisolated static func runHelper(
        _ kind: DashboardRefresh,
        executable: URL,
        lifecycle: HelperLifecycle,
        timeout: TimeInterval = 20
    ) -> Result<DashboardSnapshot, Error> {
        let deadline = DispatchTime.now() + timeout
        let process = Process()
        let stdout = Pipe()
        let stderr = Pipe()
        let exited = DispatchSemaphore(value: 0)
        process.executableURL = executable
        process.arguments = kind.arguments
        process.environment = childEnvironment()
        process.currentDirectoryURL = FileManager.default.homeDirectoryForCurrentUser
        process.standardOutput = stdout
        process.standardError = stderr
        process.terminationHandler = { _ in exited.signal() }

        guard lifecycle.prepare(process) else {
            return .failure(DashboardDataError.invalid("Dashboard stopped."))
        }
        defer { lifecycle.finish(process) }

        do {
            try process.run()
            let stopRequested = lifecycle.didRun(process)
            guard let active = lifecycle.activeTarget(for: process) else {
                throw DashboardDataError.invalid("Dashboard stopped.")
            }
            let output = CapturedOutput(stdout: stdout.fileHandleForReading, stderr: stderr.fileHandleForReading)
            defer { output.close() }
            if stopRequested, process.isRunning { process.terminate() }

            let exitedBeforeDeadline = exited.wait(timeout: deadline) == .success
            guard exitedBeforeDeadline, !process.isRunning, output.wait(until: deadline) else {
                forceStop(active, retainedDescendants: [])
                output.close()
                _ = output.wait(until: .now() + 1)
                throw DashboardDataError.invalid("Dashboard helper timed out.")
            }
            guard !output.stdoutOverflow else { throw DashboardDataError.invalid("Dashboard response exceeded 1 MB.") }
            guard process.terminationStatus == 0 else {
                let detail = String(decoding: output.stderr, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
                throw DashboardDataError.invalid(detail.isEmpty ? "Dashboard helper exited unexpectedly." : detail)
            }
            return .success(try DashboardSnapshot.decode(output.stdout))
        } catch {
            return .failure(error)
        }
    }

    private nonisolated static func childEnvironment() -> [String: String] {
        var environment = ProcessInfo.processInfo.environment
        environment["AIUSAGE_MACOS_HOST_HELPER"] = "1"
        let standardPaths = ["/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"]
        let inheritedPaths = environment["PATH", default: ""].split(separator: ":").map(String.init)
        environment["PATH"] = (standardPaths + inheritedPaths).reduce(into: [String]()) {
            if !$0.contains($1) { $0.append($1) }
        }.joined(separator: ":")
        return environment
    }
}

struct ActiveHelper: @unchecked Sendable {
    let process: Process
    let pid: pid_t
    let identity: DarwinProcessIdentity?
}

struct HelperTerminationState {
    let target: ActiveHelper?
    let isPrepared: Bool
}

private struct DescendantTarget: Sendable {
    let identity: DarwinProcessIdentity
    let depth: Int
}

final class HelperLifecycle: @unchecked Sendable {
    private let lock = NSLock()
    private var active: ActiveHelper?
    private var retainedTerminationTarget: ActiveHelper?
    private var stopping = false

    var terminationState: HelperTerminationState {
        lock.lock()
        defer { lock.unlock() }
        let target = retainedTerminationTarget
        return HelperTerminationState(
            target: target,
            isPrepared: target?.pid == 0 && active?.process === target?.process && active?.pid == 0
        )
    }

    func prepare(_ process: Process) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard !stopping else { return false }
        active = ActiveHelper(process: process, pid: 0, identity: nil)
        return true
    }

    func didRun(_ process: Process) -> Bool {
        let pid = process.processIdentifier
        let started = ActiveHelper(process: process, pid: pid, identity: darwinProcessIdentity(pid))
        lock.lock()
        defer { lock.unlock() }
        guard active?.process === process else { return stopping }
        active = started
        if stopping { retainedTerminationTarget = started }
        return stopping
    }

    func activeTarget(for process: Process) -> ActiveHelper? {
        lock.lock()
        defer { lock.unlock() }
        return active?.process === process ? active : nil
    }

    func finish(_ process: Process) {
        lock.lock()
        if active?.process === process { active = nil }
        lock.unlock()
    }

    func beginStop() -> ActiveHelper? {
        lock.lock()
        stopping = true
        retainedTerminationTarget = active
        defer { lock.unlock() }
        return retainedTerminationTarget
    }
}

private final class CapturedOutput {
    private static let limit = 1 << 20
    private let group = DispatchGroup()
    private let lock = NSLock()
    private let stdoutHandle: FileHandle
    private let stderrHandle: FileHandle
    private var outputData = Data()
    private var errorData = Data()
    private var outputOverflow = false

    init(stdout: FileHandle, stderr: FileHandle) {
        stdoutHandle = stdout
        stderrHandle = stderr
        drain(stdout, isStdout: true)
        drain(stderr, isStdout: false)
    }

    var stdout: Data { locked { outputData } }
    var stderr: Data { locked { errorData } }
    var stdoutOverflow: Bool { locked { outputOverflow } }

    func wait(until deadline: DispatchTime) -> Bool {
        group.wait(timeout: deadline) == .success
    }

    func close() {
        try? stdoutHandle.close()
        try? stderrHandle.close()
    }

    private func drain(_ handle: FileHandle, isStdout: Bool) {
        group.enter()
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            defer { group.leave() }
            while true {
                let chunk = (try? handle.read(upToCount: 8192)) ?? nil
                guard let chunk, !chunk.isEmpty else { return }
                lock.lock()
                if isStdout {
                    let available = max(0, Self.limit - outputData.count)
                    outputData.append(chunk.prefix(available))
                    outputOverflow = outputOverflow || chunk.count > available
                } else {
                    let available = max(0, Self.limit - errorData.count)
                    errorData.append(chunk.prefix(available))
                }
                lock.unlock()
            }
        }
    }

    private func locked<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }
}

@MainActor
private final class DashboardPanel: NSPanel {
    var onCancel: (() -> Void)?

    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }
    override func cancelOperation(_ sender: Any?) { onCancel?() }
}

@MainActor
private final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    private let panel = DashboardPanel(
        contentRect: NSRect(x: 0, y: 0, width: DashboardView.preferredWidth, height: 300),
        styleMask: .borderless,
        backing: .buffered,
        defer: false
    )
    private var store: DashboardStore?
    private var statusItemClickMonitor: Any?
    private var outsideClickMonitor: Any?
    private var panelVisibilityGeneration = 0

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        guard let button = statusItem.button else { return NSApp.terminate(nil) }
        let symbol = NSImage(systemSymbolName: "chart.bar.xaxis", accessibilityDescription: "AI Usage")
        symbol?.isTemplate = true
        button.image = symbol
        button.toolTip = "AI Usage"
        button.setAccessibilityLabel("AI Usage")
        button.target = self
        button.action = #selector(statusItemClicked(_:))
        button.sendAction(on: [.leftMouseUp, .rightMouseUp])
        installStatusItemClickMonitor()

        panel.delegate = self
        panel.isFloatingPanel = true
        panel.level = .popUpMenu
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.hidesOnDeactivate = false
        panel.isReleasedWhenClosed = false
        panel.collectionBehavior = [.transient, .moveToActiveSpace]
        panel.onCancel = { [weak self] in self?.hidePanel() }

        guard let executable = bundledCLI() else {
            showMissingCLIAndTerminate()
            return
        }
        let store = DashboardStore(executable: executable)
        self.store = store
        let view = DashboardView(store: store) { [weak self] size in self?.resizePanel(to: size) }
        install(view: view)
        store.start()

        if ProcessInfo.processInfo.environment["AIUSAGE_SHOW_ON_LAUNCH"] == "1" {
            DispatchQueue.main.async { [weak self] in self?.showPanelWhenReady() }
        }
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        hidePanel()
        guard let store, store.isRefreshing else { return .terminateNow }
        store.stop { NSApp.reply(toApplicationShouldTerminate: true) }
        return .terminateLater
    }

    func applicationDidResignActive(_ notification: Notification) {
        hidePanel()
    }

    func applicationWillTerminate(_ notification: Notification) {
        hidePanel()
        removeStatusItemClickMonitor()
        store?.stop {}
    }

    func windowDidResignKey(_ notification: Notification) {
        let generation = panelVisibilityGeneration
        DispatchQueue.main.async { [weak self] in
            guard let self,
                  generation == self.panelVisibilityGeneration,
                  self.panel.isVisible,
                  !self.panel.isKeyWindow else { return }
            self.hidePanel()
        }
    }

    @objc private func statusItemClicked(_ sender: NSStatusBarButton) {
        guard let event = NSApp.currentEvent else {
            panel.isVisible ? hidePanel() : showPanel()
            return
        }
        handleStatusItemClick(sender, event: event)
    }

    private func handleStatusItemClick(_ button: NSStatusBarButton, event: NSEvent) {
        if event.type == .rightMouseDown || event.type == .rightMouseUp {
            hidePanel()
            showQuitMenu(from: button, event: event)
        } else {
            panel.isVisible ? hidePanel() : showPanel()
        }
    }

    private func install(view: DashboardView) {
        let effectView = NSVisualEffectView()
        effectView.material = .popover
        effectView.blendingMode = .behindWindow
        effectView.state = .active
        effectView.wantsLayer = true
        effectView.layer?.cornerRadius = 12
        effectView.layer?.cornerCurve = .continuous
        effectView.layer?.masksToBounds = true
        effectView.layer?.borderWidth = 0.5
        effectView.layer?.borderColor = NSColor.separatorColor.withAlphaComponent(0.5).cgColor

        let hostingController = NSHostingController(rootView: view)
        hostingController.view.translatesAutoresizingMaskIntoConstraints = false
        let container = NSViewController()
        container.view = effectView
        container.addChild(hostingController)
        effectView.addSubview(hostingController.view)
        NSLayoutConstraint.activate([
            hostingController.view.leadingAnchor.constraint(equalTo: effectView.leadingAnchor),
            hostingController.view.trailingAnchor.constraint(equalTo: effectView.trailingAnchor),
            hostingController.view.topAnchor.constraint(equalTo: effectView.topAnchor),
            hostingController.view.bottomAnchor.constraint(equalTo: effectView.bottomAnchor)
        ])
        panel.contentViewController = container
    }

    private func showPanel() {
        guard statusButtonFrame() != nil else { return }
        panelVisibilityGeneration += 1
        positionPanel()
        setStatusItemSelected(true)
        NSApp.activate(ignoringOtherApps: true)
        panel.makeKeyAndOrderFront(nil)
        installOutsideClickMonitor()
    }

    private func hidePanel() {
        panelVisibilityGeneration += 1
        removeOutsideClickMonitor()
        if panel.isVisible { panel.orderOut(nil) }
        setStatusItemSelected(false)
    }

    private func setStatusItemSelected(_ selected: Bool) {
        statusItem.button?.highlight(selected)
    }

    private func installStatusItemClickMonitor() {
        statusItemClickMonitor = NSEvent.addLocalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] event in
            guard let self,
                  let button = self.statusItem.button,
                  event.window === button.window,
                  button.bounds.contains(button.convert(event.locationInWindow, from: nil)) else { return event }
            self.handleStatusItemClick(button, event: event)
            return nil
        }
    }

    private func removeStatusItemClickMonitor() {
        guard let statusItemClickMonitor else { return }
        NSEvent.removeMonitor(statusItemClickMonitor)
        self.statusItemClickMonitor = nil
    }

    private func showPanelWhenReady() {
        guard let button = statusItem.button else { return }
        button.window?.contentView?.layoutSubtreeIfNeeded()
        guard let anchor = statusButtonFrame(),
              let screen = button.window?.screen,
              !button.bounds.isEmpty,
              anchor.maxY >= screen.frame.maxY - 12 else {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { [weak self] in
                self?.showPanelWhenReady()
            }
            return
        }
        showPanel()
    }

    private func installOutsideClickMonitor() {
        removeOutsideClickMonitor()
        outsideClickMonitor = NSEvent.addGlobalMonitorForEvents(
            matching: [.leftMouseDown, .rightMouseDown, .otherMouseDown]
        ) { [weak self] _ in
            DispatchQueue.main.async { self?.hidePanel() }
        }
    }

    private func removeOutsideClickMonitor() {
        guard let outsideClickMonitor else { return }
        NSEvent.removeMonitor(outsideClickMonitor)
        self.outsideClickMonitor = nil
    }

    private func statusButtonFrame() -> NSRect? {
        guard let button = statusItem.button, let window = button.window else { return nil }
        return window.convertToScreen(button.convert(button.bounds, to: nil))
    }

    private func positionPanel() {
        guard let anchor = statusButtonFrame(),
              let screen = statusItem.button?.window?.screen ?? NSScreen.screens.first(where: { $0.frame.intersects(anchor) }) else { return }
        let visible = screen.visibleFrame
        let size = panel.frame.size
        let maxX = max(visible.minX, visible.maxX - size.width)
        let maxY = max(visible.minY, visible.maxY - size.height)
        let x = min(max(anchor.midX - size.width / 2, visible.minX), maxX)
        let y = min(max(anchor.minY - 4 - size.height, visible.minY), maxY)
        panel.setFrameOrigin(NSPoint(x: x, y: y))
    }

    private func bundledCLI() -> URL? {
        guard let executable = Bundle.main.url(forAuxiliaryExecutable: "aiusage-cli"),
              FileManager.default.isExecutableFile(atPath: executable.path) else { return nil }
        return executable
    }

    private func showMissingCLIAndTerminate() {
        let alert = NSAlert()
        alert.alertStyle = .critical
        alert.messageText = "AI Usage cannot start"
        alert.informativeText = "The bundled aiusage-cli executable is missing or cannot be run. Reinstall AI Usage."
        alert.addButton(withTitle: "Quit")
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
        NSApp.terminate(nil)
    }

    private func resizePanel(to size: CGSize) {
        guard size.height > 0 else { return }
        // Refresh swaps data and controls in one render pass; keep an open panel's geometry stable.
        if panel.isVisible, store?.isRefreshing == true { return }
        let screen = statusItem.button?.window?.screen ?? NSScreen.main
        let maxHeight = max(220, (screen?.visibleFrame.height ?? 700) - 32)
        let contentSize = NSSize(width: DashboardView.preferredWidth, height: min(ceil(size.height), maxHeight))
        guard abs(panel.frame.height - contentSize.height) > 0.5 else { return }
        panel.setContentSize(contentSize)
        if panel.isVisible { positionPanel() }
    }

    private func showQuitMenu(from button: NSStatusBarButton, event: NSEvent) {
        let menu = NSMenu()
        let quit = NSMenuItem(title: "Quit AI Usage", action: #selector(quitApplication), keyEquivalent: "q")
        quit.target = self
        menu.addItem(quit)
        NSMenu.popUpContextMenu(menu, with: event, for: button)
    }

    @objc private func quitApplication() { NSApp.terminate(nil) }
}

@main
private struct AiUsageApplication {
    @MainActor
    static func main() {
        let application = NSApplication.shared
        let delegate = AppDelegate()
        application.delegate = delegate
        application.run()
        withExtendedLifetime(delegate) {}
    }
}
