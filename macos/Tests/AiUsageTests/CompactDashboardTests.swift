import AppKit
import SwiftUI
import XCTest
@testable import AiUsage

final class CompactDashboardTests: XCTestCase {
    @MainActor
    func testAllProviderCompactDashboardAndOptionalPreview() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = directory.appendingPathComponent("snapshot.json")
        let helper = directory.appendingPathComponent("helper")
        try Data("#!/bin/sh\nexec /bin/cat '\(fixture.path)'\n".utf8).write(to: helper)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: helper.path)
        let now = Int64(Date().timeIntervalSince1970)
        let windows: [(String, String, String, Double, Int64?)] = [
            ("Claude", "Ultra", "5-hour session", 95, 240),
            ("Claude", "Ultra", "Weekly · all", 98, 604800),
            ("Claude", "Ultra", "Weekly · Fable", 100, 604800),
            ("OpenAI", "Codex Pro", "Weekly", 50, 345600),
            ("Kimi", "Code", "5-hour", 100, 16440),
            ("Kimi", "Code", "Weekly", 100, 604800),
            ("Z.AI", "Lite", "5-hour", 100, nil),
            ("Z.AI", "Lite", "Weekly", 100, 432000),
            ("Antigravity", "Google", "Gemini · 5-hour", 99.75, 5760),
            ("Antigravity", "Google", "Claude + GPT · 5-hour", 100, 17700),
            ("Antigravity", "Google", "Gemini · Weekly", 99.88, 432000),
            ("Antigravity", "Google", "Claude + GPT · Weekly", 100, 604800),
            ("Antigravity", "Google", "Gemini 4 Argon · Weekly", 87.39, 604800)
        ]
        let quotas: [[String: Any]] = windows.enumerated().map { index, item in
            let (provider, product, window, value, reset) = item
            var row: [String: Any] = ["id": "fixture:\(index)", "provider": provider, "product": product,
                "window": window, "remainingPercent": value, "elapsedPercent": 35,
                "updatedAt": now - 120, "attemptedAt": now - 120, "failure": "", "source": "fixture", "detail": "", "stale": index == 6]
            if let reset { row["resetAt"] = now + reset }
            return row
        }
        func benefit(_ id: String, provider: String, product: String, group: [String: Any]) -> [String: Any] {
            ["id": id, "provider": provider, "product": product, "group": group,
             "updatedAt": now - 120, "attemptedAt": now - 120, "failure": "", "source": "fixture", "stale": false]
        }
        let benefits = [
            benefit("promo", provider: "Claude", product: "Ultra", group: ["kind": "api_promo"]),
            benefit("resets", provider: "Claude", product: "Ultra", group: ["kind": "limit_resets"]),
            benefit("credits", provider: "OpenAI", product: "Codex Pro", group: ["kind": "credits", "total": 62500]),
            benefit("banked", provider: "OpenAI", product: "Codex Pro", group: ["kind": "banked_resets", "total": 1,
                "grants": [["id": "reset", "label": "Reset", "amount": 1, "expiresAt": now + 7 * 86400]]])
        ]
        try JSONSerialization.data(withJSONObject: ["version": 1, "generatedAt": now, "state": "ready", "message": "", "quotas": quotas, "benefits": benefits]).write(to: fixture)
        let store = DashboardStore(executable: helper)
        defer { store.stop {} }
        let suite = "aiusage.compact.preview.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let visibility = RowVisibility(defaults: defaults)
        store.request(.cache)
        let deadline = Date().addingTimeInterval(3)
        while store.snapshot == nil && Date() < deadline { RunLoop.main.run(until: Date().addingTimeInterval(0.02)) }
        XCTAssertEqual(store.groups.count, 5)
        let snapshot = try XCTUnwrap(store.snapshot)
        let antigravity = try XCTUnwrap(store.groups.last)
        for quota in antigravity.quotas where quota.window.contains("Claude + GPT") {
            visibility.setHidden(true, id: RowVisibility.key(quota), in: antigravity)
        }
        let host = NSHostingView(rootView: DashboardView(store: store, onSizeChange: { _ in }, visibility: visibility)
            .background(Color(NSColor.windowBackgroundColor)))
        host.frame = NSRect(origin: .zero, size: host.fittingSize)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        XCTAssertEqual(host.fittingSize.width, 430, accuracy: 0.5)
        XCTAssertGreaterThan(host.fittingSize.height, 400)
        XCTAssertEqual(store.snapshot, snapshot, "Presentation must not round or hide source data")
        if let path = ProcessInfo.processInfo.environment["AIUSAGE_COMPACT_PREVIEW"] {
            let bitmap = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
            host.cacheDisplay(in: host.bounds, to: bitmap)
            try XCTUnwrap(bitmap.representation(using: .png, properties: [:])).write(to: URL(fileURLWithPath: path))
        }
    }
}
