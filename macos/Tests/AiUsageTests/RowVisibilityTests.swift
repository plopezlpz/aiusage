import AppKit
import SwiftUI
import XCTest
@testable import AiUsage

final class RowVisibilityTests: XCTestCase {
    func quota(_ provider: String = "Claude") -> DashboardQuota {
        DashboardQuota(id: "\(provider):Weekly", provider: provider, product: "Plan", window: "Weekly", remainingPercent: 70, elapsedPercent: nil, resetAt: nil, updatedAt: nil, attemptedAt: nil, failure: "", source: "fixture", detail: "", stale: false)
    }

    func testPersistenceIsolationAndTemporaryReveal() throws {
        let suite = "aiusage.visibility.tests.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let visibility = RowVisibility(defaults: defaults)
        let claude = quota()
        // Deliberately reuse the quota ID to verify benefit/ quota namespaces.
        let benefit = DashboardBenefit(id: claude.id, provider: "Claude", product: "Plan", group: BenefitGroup(kind: "credits", total: 25, unlimited: nil, grants: nil, note: nil), updatedAt: nil, attemptedAt: nil, failure: "", source: "fixture", stale: false)
        let group = QuotaGroup(provider: "Claude", product: "Plan", quotas: [claude], benefits: [benefit])
        let other = QuotaGroup(provider: "Antigravity", product: "Google", quotas: [quota("Antigravity")])
        let key = RowVisibility.key(claude)
        visibility.setHidden(true, id: key, in: group)
        XCTAssertFalse(visibility.isVisible(key, in: group))
        XCTAssertTrue(visibility.isVisible(RowVisibility.key(benefit), in: group))
        XCTAssertTrue(visibility.isVisible(RowVisibility.key(other.quotas[0]), in: other))
        XCTAssertEqual(visibility.hiddenCount(in: group), 1)
        visibility.toggleReveal(group)
        XCTAssertTrue(visibility.isVisible(key, in: group))
        XCTAssertTrue(visibility.hiddenIDs.contains(key), "Reveal must not permanently unhide")
        let restarted = RowVisibility(defaults: defaults)
        XCTAssertEqual(restarted.hiddenIDs, [key])
        XCTAssertTrue(restarted.revealedGroups.isEmpty)
        XCTAssertFalse(restarted.isVisible(key, in: group))
        visibility.toggleReveal(group)
        XCTAssertFalse(visibility.isVisible(key, in: group))
        visibility.setHidden(true, id: RowVisibility.key(benefit), in: group)
        XCTAssertEqual(visibility.hiddenCount(in: group), 2)
        let absent = QuotaGroup(provider: "Claude", product: "Plan", quotas: [])
        XCTAssertEqual(visibility.hiddenCount(in: absent), 0)
        XCTAssertEqual(visibility.hiddenIDs.count, 2, "Temporary missing data must not erase saved preferences")
        visibility.toggleReveal(group)
        visibility.setHidden(false, id: key, in: group)
        XCTAssertTrue(visibility.revealedGroups.contains(group.id))
        visibility.setHidden(false, id: RowVisibility.key(benefit), in: group)
        XCTAssertFalse(visibility.revealedGroups.contains(group.id), "No invisible reveal mode after last unhide")
        XCTAssertTrue(RowVisibility(defaults: defaults).hiddenIDs.isEmpty)
    }

    func testMalformedPreferenceIsIgnored() throws {
        let suite = "aiusage.visibility.invalid-tests.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        defaults.set(23, forKey: RowVisibility.preferenceKey)
        XCTAssertTrue(RowVisibility(defaults: defaults).hiddenIDs.isEmpty)
    }

    @MainActor
    func testHiddenBenefitStaysReadableAndOptionalPreview() throws {
        let benefit = DashboardBenefit(id: "promo", provider: "Claude", product: "Ultra", group: BenefitGroup(kind: "api_promo", total: nil, unlimited: nil, grants: nil, note: "Fixture only"), updatedAt: nil, attemptedAt: nil, failure: "", source: "fixture", stale: false)
        let root = VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Claude · Ultra — fixture").font(.headline)
                Spacer()
                HiddenItemsButton(count: 1, revealed: true, provider: "Claude", action: {})
            }.padding(.horizontal, 14)
            VisibilityRow(hidden: true, title: "API promotional credits", setHidden: { _ in }) {
                BenefitRow(benefit: benefit, expandedIDs: .constant([]))
            }
        }
        .padding(.vertical, 12)
        .frame(width: DashboardView.preferredWidth)
        .background(Color(NSColor.windowBackgroundColor))
        let host = NSHostingView(rootView: root)
        let size = host.fittingSize
        XCTAssertEqual(size.width, DashboardView.preferredWidth, accuracy: 1)
        XCTAssertGreaterThan(size.height, 100)
        XCTAssertLessThan(size.height, 240)
        host.frame = NSRect(origin: .zero, size: size)
        host.layoutSubtreeIfNeeded()
        if let path = ProcessInfo.processInfo.environment["AIUSAGE_HIDDEN_BENEFIT_PREVIEW"] {
            let bitmap = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
            host.cacheDisplay(in: host.bounds, to: bitmap)
            try XCTUnwrap(bitmap.representation(using: .png, properties: [:])).write(to: URL(fileURLWithPath: path))
        }
    }
}
