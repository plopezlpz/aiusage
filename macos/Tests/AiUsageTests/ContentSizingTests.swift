import AppKit
import SwiftUI
import XCTest
@testable import AiUsage

final class ContentSizingTests: XCTestCase {
    @MainActor
    func testFullDashboardResizesHostedPanelBeyondHeader() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = directory.appendingPathComponent("snapshot.json")
        let helper = directory.appendingPathComponent("helper")
        try Data("#!/bin/sh\nexec /bin/cat '\(fixture.path)'\n".utf8).write(to: helper)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: helper.path)
        let store = DashboardStore(executable: helper)
        defer { store.stop {} }
        let panel = NSPanel(contentRect: NSRect(x: -10000, y: -10000, width: DashboardView.preferredWidth, height: 64), styleMask: [.borderless], backing: .buffered, defer: false)
        panel.isReleasedWhenClosed = false
        defer { panel.close() }
        let maximumPanelHeight = max(220, (NSScreen.screens.map(\.visibleFrame.height).min() ?? 700) - 32)
        let suite = "aiusage.visibility.panel-tests.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let visibility = RowVisibility(defaults: defaults)
        let root = DashboardView(store: store, onSizeChange: { size in
            DispatchQueue.main.async {
                panel.setContentSize(NSSize(width: DashboardView.preferredWidth, height: min(ceil(size.height), maximumPanelHeight)))
            }
        }, visibility: visibility)
        let host = NSHostingController(rootView: root)
        let container = NSViewController()
        container.view = NSVisualEffectView()
        container.addChild(host)
        host.view.translatesAutoresizingMaskIntoConstraints = false
        container.view.addSubview(host.view)
        NSLayoutConstraint.activate([
            host.view.leadingAnchor.constraint(equalTo: container.view.leadingAnchor),
            host.view.trailingAnchor.constraint(equalTo: container.view.trailingAnchor),
            host.view.topAnchor.constraint(equalTo: container.view.topAnchor),
            host.view.bottomAnchor.constraint(equalTo: container.view.bottomAnchor)
        ])
        panel.contentViewController = container
        panel.orderFront(nil)

        func loadRows(_ count: Int) throws {
            let now = Int64(Date().timeIntervalSince1970)
            let quotas: [[String: Any]] = (0..<count).map { index in
                ["id": "fixture:\(index)", "provider": "Antigravity", "product": "Google",
                 "window": index == 0 ? "Gemini · Weekly" : index == 1 ? "Claude + GPT · 5-hour" : "Model \(index) · Weekly", "remainingPercent": 75,
                 "updatedAt": now, "attemptedAt": now, "failure": "", "source": "fixture",
                 "detail": "", "stale": false]
            }
            let json: [String: Any] = ["version": 1, "generatedAt": now, "state": "ready", "message": "", "quotas": quotas]
            try JSONSerialization.data(withJSONObject: json).write(to: fixture)
            store.request(.cache)
            let deadline = Date().addingTimeInterval(3)
            repeat {
                RunLoop.main.run(until: Date().addingTimeInterval(0.02))
                container.view.layoutSubtreeIfNeeded()
            } while Date() < deadline && (store.snapshot?.quotas.count != count || store.isRefreshing)
            // Allow preference-driven AppKit resizing to settle as it does in production.
            RunLoop.main.run(until: Date().addingTimeInterval(0.1))
            XCTAssertEqual(store.snapshot?.quotas.count, count, "\(String(describing: store.transportError))")
        }
        try loadRows(2)
        let smallHeight = panel.frame.height
        XCTAssertGreaterThan(smallHeight, 130, "Dashboard must not collapse to its header")
        try loadRows(8)
        XCTAssertGreaterThan(panel.frame.height, smallHeight + 100)
        try loadRows(100)
        XCTAssertGreaterThan(panel.frame.height, smallHeight + 100)
        XCTAssertLessThanOrEqual(panel.frame.height, maximumPanelHeight)
        try loadRows(2)
        XCTAssertEqual(panel.frame.height, smallHeight, accuracy: 2)
        let group = try XCTUnwrap(store.groups.first)
        let firstKey = RowVisibility.key(group.quotas[0])
        visibility.setHidden(true, id: firstKey, in: group)
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        XCTAssertLessThan(panel.frame.height, smallHeight)
        XCTAssertEqual(store.snapshot?.quotas.count, 2, "Hiding must not mutate collected data")
        visibility.toggleReveal(group)
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        XCTAssertGreaterThan(panel.frame.height, smallHeight, "Revealed row includes its hidden-state annotation")
        if let path = ProcessInfo.processInfo.environment["AIUSAGE_HIDDEN_PREVIEW"] {
            let view = container.view
            let bitmap = try XCTUnwrap(view.bitmapImageRepForCachingDisplay(in: view.bounds))
            view.cacheDisplay(in: view.bounds, to: bitmap)
            try XCTUnwrap(bitmap.representation(using: .png, properties: [:])).write(to: URL(fileURLWithPath: path))
        }
        visibility.toggleReveal(group)
        visibility.setHidden(true, id: RowVisibility.key(group.quotas[1]), in: group)
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        XCTAssertGreaterThan(panel.frame.height, 90, "All-hidden provider must retain header and recovery control")
        XCTAssertLessThan(panel.frame.height, smallHeight)
        try loadRows(2)
        XCTAssertEqual(visibility.hiddenCount(in: try XCTUnwrap(store.groups.first)), 2)
        for quota in group.quotas { visibility.setHidden(false, id: RowVisibility.key(quota), in: group) }
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        XCTAssertEqual(panel.frame.height, smallHeight, accuracy: 2)
        if let path = ProcessInfo.processInfo.environment["AIUSAGE_PANEL_PREVIEW"] {
            let view = container.view
            let bitmap = try XCTUnwrap(view.bitmapImageRepForCachingDisplay(in: view.bounds))
            view.cacheDisplay(in: view.bounds, to: bitmap)
            let data = try XCTUnwrap(bitmap.representation(using: .png, properties: [:]))
            try data.write(to: URL(fileURLWithPath: path))
        }
    }

    @MainActor
    func testMeasuredContentGrowsShrinksAndCapsAtAvailableHeight() {
        let model = SizingFixture()
        let host = NSHostingView(rootView: SizingPreview(model: model))
        host.frame = NSRect(x: 0, y: 0, width: DashboardView.preferredWidth, height: 600)

        func expectHeight(_ height: CGFloat, file: StaticString = #filePath, line: UInt = #line) {
            let deadline = Date().addingTimeInterval(2)
            repeat {
                host.layoutSubtreeIfNeeded()
                if abs(host.fittingSize.height - height) < 1 { break }
                RunLoop.main.run(until: Date().addingTimeInterval(0.01))
            } while Date() < deadline
            XCTAssertEqual(host.fittingSize.height, height, accuracy: 1, file: file, line: line)
            XCTAssertEqual(host.fittingSize.width, DashboardView.preferredWidth, accuracy: 1, file: file, line: line)
        }

        expectHeight(120)
        model.height = 350
        expectHeight(350)
        model.height = 900
        expectHeight(500)
        model.height = 80
        expectHeight(80)
        model.maximum = 60
        expectHeight(60)
    }
}

@MainActor
private final class SizingFixture: ObservableObject {
    @Published var height: CGFloat = 120
    @Published var maximum: CGFloat = 500
}

private struct SizingPreview: View {
    @ObservedObject var model: SizingFixture

    var body: some View {
        ContentFittingScrollView(maximumHeight: model.maximum) {
            Color.clear.frame(height: model.height)
        }
        .frame(width: DashboardView.preferredWidth)
        .fixedSize(horizontal: false, vertical: true)
    }
}
