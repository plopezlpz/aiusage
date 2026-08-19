import Darwin
import Foundation
import XCTest
@testable import AiUsage

final class DashboardSnapshotTests: XCTestCase {
    func testDecodingValidationAndStableGrouping() throws {
        let generated: Int64 = 1_735_689_600
        let json = """
        {
          "version": 1,
          "generatedAt": \(generated),
          "state": "partial",
          "message": "Kimi unavailable",
          "quotas": [
            {"id":"Claude:5-hour session","provider":"Claude","product":"Code","window":"5-hour session","remainingPercent":82.5,"resetAt":1735696800,"updatedAt":1735689590,"attemptedAt":1735689595,"failure":"","source":"status line","detail":"","stale":false},
            {"id":"OpenAI:Weekly","provider":"OpenAI","product":"Codex Plus","window":"Weekly","remainingPercent":20,"resetAt":1735783200,"updatedAt":1735689500,"attemptedAt":1735689550,"failure":"temporary failure","source":"app-server","detail":"Plan type: plus","stale":true},
            {"id":"Claude:Weekly","provider":"Claude","product":"Code","window":"Weekly","remainingPercent":55,"resetAt":1735783200,"updatedAt":1735689590,"attemptedAt":1735689595,"failure":"","source":"status line","detail":"","stale":false}
          ]
        }
        """
        let now = Date(timeIntervalSince1970: TimeInterval(generated))
        let snapshot = try DashboardSnapshot.decode(Data(json.utf8), now: now)

        XCTAssertEqual(snapshot.state, "partial")
        XCTAssertEqual(snapshot.message, "Kimi unavailable")
        let groups = QuotaGroup.stableGroups(snapshot.quotas)
        XCTAssertEqual(groups.map(\.provider), ["Claude", "OpenAI"])
        XCTAssertEqual(groups[0].quotas.map(\.window), ["5-hour session", "Weekly"])
        XCTAssertTrue(groups[1].quotas[0].stale)

        let duplicate = json.replacingOccurrences(of: "OpenAI:Weekly", with: "Claude:5-hour session")
        XCTAssertThrowsError(try DashboardSnapshot.decode(Data(duplicate.utf8), now: now))
        let outOfRange = json.replacingOccurrences(of: "\"remainingPercent\":20", with: "\"remainingPercent\":101")
        XCTAssertThrowsError(try DashboardSnapshot.decode(Data(outOfRange.utf8), now: now))
        let unsupported = json.replacingOccurrences(of: "\"version\": 1", with: "\"version\": 2")
        XCTAssertThrowsError(try DashboardSnapshot.decode(Data(unsupported.utf8), now: now))
        let oldSnapshot = json.replacingOccurrences(of: "\"generatedAt\": \(generated)", with: "\"generatedAt\": \(generated - 301)")
        XCTAssertThrowsError(try DashboardSnapshot.decode(Data(oldSnapshot.utf8), now: now))
    }

    func testResetRefreshQueuesBehindActiveWorkUnlessForceIsPending() {
        XCTAssertEqual(queuedDashboardRefresh(active: .auto, pending: nil, incoming: .auto, resetFired: true), .auto)
        XCTAssertEqual(queuedDashboardRefresh(active: .force, pending: nil, incoming: .auto, resetFired: true), .auto)
        XCTAssertEqual(queuedDashboardRefresh(active: .auto, pending: .force, incoming: .auto, resetFired: true), .force)
        XCTAssertNil(queuedDashboardRefresh(active: .force, pending: nil, incoming: .auto))
        XCTAssertEqual(queuedDashboardRefresh(active: .cache, pending: .auto, incoming: .force), .force)
    }

    func testResetRefreshDeadlinePreservesExistingTimerUnlessReloadIsEarlier() {
        XCTAssertEqual(preferredResetRefreshDeadline(current: 110, resetDates: [115], now: 100), 110)
        XCTAssertEqual(preferredResetRefreshDeadline(current: 110, resetDates: [], now: 100), 110)
        XCTAssertEqual(preferredResetRefreshDeadline(current: 110, resetDates: [100], now: 90), 105)
        XCTAssertEqual(preferredResetRefreshDeadline(current: nil, resetDates: [115], now: 100), 120)
        XCTAssertNil(preferredResetRefreshDeadline(current: nil, resetDates: [90], now: 100))
    }

    func testQuotaAttentionIncludesStaleAndFailedValues() {
        func quota(stale: Bool = false, failure: String = "") -> DashboardQuota {
            DashboardQuota(
                id: "Claude:Weekly", provider: "Claude", product: "Code", window: "Weekly",
                remainingPercent: 50, resetAt: nil, updatedAt: nil, attemptedAt: nil,
                failure: failure, source: "status line", detail: "", stale: stale
            )
        }

        XCTAssertFalse(quotaNeedsAttention(quota()))
        XCTAssertTrue(quotaNeedsAttention(quota(stale: true)))
        XCTAssertTrue(quotaNeedsAttention(quota(failure: "temporary failure")))
        XCTAssertEqual(updateText(quota(stale: true)), "Stale · updated unknown")
        XCTAssertEqual(updateText(quota(failure: "temporary failure")), "Refresh failed unknown")
        XCTAssertEqual(updateText(quota(stale: true, failure: "temporary failure")), "Stale · refresh failed unknown")
    }

    func testDescendantDepthRejectsUnrelatedAndCyclicProcesses() {
        let parents: [pid_t: pid_t] = [40: 30, 30: 20, 20: 10, 60: 50, 70: 71, 71: 70]
        let parentOf: (pid_t) -> pid_t? = { parents[$0] }

        XCTAssertEqual(descendantDepth(pid: 40, ancestor: 10, parentOf: parentOf), 3)
        XCTAssertEqual(descendantDepth(pid: 20, ancestor: 10, parentOf: parentOf), 1)
        XCTAssertNil(descendantDepth(pid: 60, ancestor: 10, parentOf: parentOf))
        XCTAssertNil(descendantDepth(pid: 10, ancestor: 10, parentOf: parentOf))
        XCTAssertNil(descendantDepth(pid: 70, ancestor: 10, parentOf: parentOf))
    }

    func testProcessIdentityRequiresPIDAndStartTimeMatch() {
        let expected = DarwinProcessIdentity(pid: 42, startSeconds: 100, startMicroseconds: 200)

        XCTAssertTrue(processIdentityMatches(expected, expected))
        XCTAssertFalse(processIdentityMatches(expected, nil))
        XCTAssertFalse(processIdentityMatches(expected, DarwinProcessIdentity(pid: 43, startSeconds: 100, startMicroseconds: 200)))
        XCTAssertFalse(processIdentityMatches(expected, DarwinProcessIdentity(pid: 42, startSeconds: 101, startMicroseconds: 200)))
        XCTAssertFalse(processIdentityMatches(expected, DarwinProcessIdentity(pid: 42, startSeconds: 100, startMicroseconds: 201)))
    }

    func testTerminationStateAtomicallyTracksPreparedAndStartedTarget() {
        let lifecycle = HelperLifecycle()
        let run = HelperRun()
        let identity = DarwinProcessIdentity(pid: 42, startSeconds: 100, startMicroseconds: 200)

        XCTAssertTrue(lifecycle.prepare(run))
        XCTAssertEqual(lifecycle.beginStop()?.pid, 0)
        XCTAssertTrue(lifecycle.terminationState.isPrepared)
        XCTAssertTrue(lifecycle.didSpawn(run, pid: identity.pid, identity: identity))

        let started = lifecycle.terminationState
        XCTAssertFalse(started.isPrepared)
        XCTAssertEqual(started.target?.identity, identity)

        lifecycle.finish(run)
        let finished = lifecycle.terminationState
        XCTAssertFalse(finished.isPrepared)
        XCTAssertEqual(finished.target?.identity, identity)
    }

    func testHelperExecutableIsSessionLeader() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }

        let helper = directory.appendingPathComponent("helper.sh")
        let now = Int64(Date().timeIntervalSince1970)
        try """
        #!/bin/sh
        cat <<'JSON'
        {"version":1,"generatedAt":\(now),"state":"ready","message":"","quotas":[]}
        JSON
        """.write(to: helper, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: helper.path)

        let result = DashboardStore.runHelper(.cache, executable: helper, lifecycle: HelperLifecycle(), timeout: 2)
        guard case .success(let snapshot) = result else { return XCTFail("helper could not create an isolated session: \(result)") }
        XCTAssertEqual(snapshot.version, 1)
    }

    func testHelperWorksWithClosedStandardOutputDescriptors() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }

        let helper = directory.appendingPathComponent("helper.sh")
        let now = Int64(Date().timeIntervalSince1970)
        try """
        #!/bin/sh
        cat <<'JSON'
        {"version":1,"generatedAt":\(now),"state":"ready","message":"","quotas":[]}
        JSON
        """.write(to: helper, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: helper.path)

        let savedStdout = dup(STDOUT_FILENO)
        let savedStderr = dup(STDERR_FILENO)
        XCTAssertGreaterThan(savedStdout, STDERR_FILENO)
        XCTAssertGreaterThan(savedStderr, STDERR_FILENO)
        let result: Result<DashboardSnapshot, Error> = {
            close(STDOUT_FILENO)
            close(STDERR_FILENO)
            defer {
                dup2(savedStdout, STDOUT_FILENO)
                dup2(savedStderr, STDERR_FILENO)
                close(savedStdout)
                close(savedStderr)
            }
            return DashboardStore.runHelper(.cache, executable: helper, lifecycle: HelperLifecycle(), timeout: 2)
        }()

        guard case .success(let snapshot) = result else { return XCTFail("helper output was not captured: \(result)") }
        XCTAssertEqual(snapshot.version, 1)
    }

    @MainActor
    func testStoreStopTerminatesNestedHelperTree() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }

        let helperPIDFile = directory.appendingPathComponent("helper.pid")
        let childPIDFile = directory.appendingPathComponent("child.pid")
        let helper = directory.appendingPathComponent("helper.sh")
        try """
        #!/bin/sh
        printf '%s\n' "$$" > '\(helperPIDFile.path)'
        sleep 30 &
        child=$!
        printf '%s\n' "$child" > '\(childPIDFile.path)'
        wait "$child"
        """.write(to: helper, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: helper.path)

        let store = DashboardStore(executable: helper)
        store.request(.cache)
        let readyDeadline = Date().addingTimeInterval(2)
        while (!FileManager.default.fileExists(atPath: helperPIDFile.path) ||
               !FileManager.default.fileExists(atPath: childPIDFile.path)), Date() < readyDeadline {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        guard FileManager.default.fileExists(atPath: helperPIDFile.path),
              FileManager.default.fileExists(atPath: childPIDFile.path) else {
            store.stop {}
            return XCTFail("nested helper did not start")
        }

        let helperPID = try XCTUnwrap(pid_t(String(contentsOf: helperPIDFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)))
        let childPID = try XCTUnwrap(pid_t(String(contentsOf: childPIDFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)))
        let stopped = expectation(description: "store stopped")
        store.stop { stopped.fulfill() }
        await fulfillment(of: [stopped], timeout: 5)

        XCTAssertNotEqual(kill(helperPID, 0), 0)
        XCTAssertNotEqual(kill(childPID, 0), 0)
    }

    func testExitedHelperCannotLeaveSessionChild() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }

        let childPIDFile = directory.appendingPathComponent("child.pid")
        let helper = directory.appendingPathComponent("helper.sh")
        try """
        #!/bin/sh
        sleep 30 &
        printf '%s\n' "$!" > '\(childPIDFile.path)'
        exit 0
        """.write(to: helper, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: helper.path)

        let result = DashboardStore.runHelper(.cache, executable: helper, lifecycle: HelperLifecycle(), timeout: 2)
        guard case .failure(let error) = result else { return XCTFail("expected helper timeout") }
        XCTAssertEqual(error.localizedDescription, "Dashboard helper timed out.")

        let childPIDText = try String(contentsOf: childPIDFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        let childPID = try XCTUnwrap(pid_t(childPIDText))
        let childExitDeadline = Date().addingTimeInterval(2)
        while kill(childPID, 0) == 0 || errno == EPERM {
            guard Date() < childExitDeadline else {
                XCTFail("exited helper left session child \(childPID) running")
                break
            }
            usleep(10_000)
        }
        XCTAssertNotEqual(kill(childPID, 0), 0)
    }

    func testHelperTimeoutDoesNotBlockNextRun() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }

        func executable(_ name: String, contents: String) throws -> URL {
            let url = directory.appendingPathComponent(name)
            try contents.write(to: url, atomically: true, encoding: .utf8)
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
            return url
        }

        let childPIDFile = directory.appendingPathComponent("child.pid")
        let quotedPIDFile = childPIDFile.path.replacingOccurrences(of: "'", with: "'\\''")
        let slow = try executable("slow.sh", contents: """
        #!/bin/sh
        sleep 30 &
        child=$!
        printf '%s\\n' "$child" > '\(quotedPIDFile)'
        wait "$child"
        """)
        let lifecycle = HelperLifecycle()
        let started = Date()
        let timedOut = DashboardStore.runHelper(.cache, executable: slow, lifecycle: lifecycle, timeout: 2)
        guard case .failure(let error) = timedOut else { return XCTFail("expected helper timeout") }
        XCTAssertEqual(error.localizedDescription, "Dashboard helper timed out.")
        XCTAssertLessThan(Date().timeIntervalSince(started), 5)

        let childPIDText = try String(contentsOf: childPIDFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        let childPID = try XCTUnwrap(pid_t(childPIDText))
        let childExitDeadline = Date().addingTimeInterval(2)
        while kill(childPID, 0) == 0 || errno == EPERM {
            guard Date() < childExitDeadline else {
                XCTFail("timed-out helper left child process \(childPID) running")
                break
            }
            usleep(10_000)
        }
        XCTAssertNotEqual(kill(childPID, 0), 0)

        let now = Int64(Date().timeIntervalSince1970)
        let fast = try executable("fast.sh", contents: """
        #!/bin/sh
        cat <<'JSON'
        {"version":1,"generatedAt":\(now),"state":"ready","message":"","quotas":[]}
        JSON
        """)
        let completed = DashboardStore.runHelper(.cache, executable: fast, lifecycle: lifecycle, timeout: 2)
        guard case .success(let snapshot) = completed else { return XCTFail("next helper run failed") }
        XCTAssertEqual(snapshot.version, 1)
    }
}
