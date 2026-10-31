import AppKit
import Foundation
import SwiftUI
import XCTest
@testable import AiUsage

final class BenefitTests: XCTestCase {
    let now: Int64 = 1_793_448_000

    func fixture() -> BenefitGroup {
        BenefitGroup(kind: "banked_resets", total: 4, unlimited: nil, grants: [
            BenefitGrant(id: "later", label: "Reset", amount: 1, expiresAt: now + 10 * 86400, noExpiry: nil),
            BenefitGrant(id: "unknown", label: "Reset", amount: 1, expiresAt: nil, noExpiry: nil),
            BenefitGrant(id: "expired", label: "Reset", amount: 1, expiresAt: now - 1, noExpiry: nil),
            BenefitGrant(id: "soon", label: "Reset", amount: 1, expiresAt: now + 86400, noExpiry: nil)
        ], note: nil)
    }

    func testSummaryOrderingWarningsAndUnknownExpiry() {
        let group = fixture()
        XCTAssertEqual(group.available(at: now), 3)
        XCTAssertEqual(group.valueText(at: now), "3 available")
        XCTAssertEqual(group.sortedGrants.map(\.id), ["expired", "soon", "later", "unknown"])
        XCTAssertTrue(group.expandable)
        XCTAssertEqual(group.expiry(at: now), BenefitExpiry(text: "Next known: expires in 1d", warning: true))
        XCTAssertTrue(benefitExpiry(now + 172800, now: now).warning)
        XCTAssertFalse(benefitExpiry(now + 172801, now: now).warning)
        XCTAssertEqual(benefitExpiry(now, now: now), BenefitExpiry(text: "Expired", warning: false))
        XCTAssertEqual(benefitExpiry(nil, now: now).text, "Expiry unknown")
        XCTAssertEqual(benefitExpiry(nil, noExpiry: true, now: now).text, "No expiration")
        XCTAssertEqual(benefitExpiry(now + 1, now: now).text, "Expires in <1m")

        let unknown = BenefitGroup(kind: "credits", total: nil, unlimited: nil, grants: nil, note: nil)
        XCTAssertEqual(unknown.valueText(at: now), "Not reported")
        XCTAssertFalse(unknown.expandable)
        let zero = BenefitGroup(kind: "banked_resets", total: 0, unlimited: nil, grants: [], note: nil)
        XCTAssertEqual(zero.valueText(at: now), "0 available")
        let unlimited = BenefitGroup(kind: "credits", total: nil, unlimited: true, grants: nil, note: nil)
        XCTAssertEqual(unlimited.valueText(at: now), "Unlimited")
    }

    func testPartialGrantListDoesNotPretendToBeComplete() {
        let grant = BenefitGrant(id: "one", label: "Reset", amount: 1, expiresAt: now + 86400, noExpiry: nil)
        let group = BenefitGroup(kind: "banked_resets", total: 3, unlimited: nil, grants: [grant], note: nil)
        XCTAssertTrue(group.expandable)
        XCTAssertEqual(group.missingAmount, 2)
        XCTAssertEqual(group.missingText, "Expiration details unavailable for 2 resets")
        XCTAssertEqual(group.expiry(at: now).text, "Next known: expires in 1d")
        let single = BenefitGroup(kind: "banked_resets", total: 1, unlimited: nil, grants: [grant], note: nil)
        XCTAssertFalse(single.expandable)
        XCTAssertEqual(single.expiry(at: now).text, "Expires in 1d")
    }

    func testAdditiveSnapshotContractAndBenefitValidation() throws {
        let json = """
        {"version":1,"generatedAt":\(now),"state":"ready","message":"","quotas":[],"benefits":[
          {"id":"Claude:benefit:api_promo","provider":"Claude","product":"Ultra","group":{"kind":"api_promo","total":82.4,"grants":[{"id":"one","label":"Promo","amount":12.4,"expiresAt":\(now+86400)},{"id":"two","label":"Promo","amount":70,"expiresAt":\(now+1209600)}]},"updatedAt":\(now),"attemptedAt":\(now),"failure":"","source":"fixture","stale":false}
        ]}
        """
        let date = Date(timeIntervalSince1970: TimeInterval(now))
        let snapshot = try DashboardSnapshot.decode(Data(json.utf8), now: date)
        let groups = QuotaGroup.stableGroups(snapshot.quotas, benefits: snapshot.benefits ?? [])
        XCTAssertEqual(groups.count, 1)
        XCTAssertEqual(groups[0].product, "Ultra")
        XCTAssertEqual(groups[0].benefits.count, 1)
        XCTAssertTrue(groups[0].quotas.isEmpty)
        XCTAssertEqual(groups[0].benefits[0].group.valueText(at: now), "$82.40")
        XCTAssertEqual(groups[0].benefits[0].group.expiry(at: now).text, "Next expires in 1d")
        for mutation in [
            json.replacingOccurrences(of: "\"total\":82.4", with: "\"total\":-1"),
            json.replacingOccurrences(of: "\"amount\":70", with: "\"amount\":100"),
            json.replacingOccurrences(of: "\"id\":\"two\"", with: "\"id\":\"one\""),
            json.replacingOccurrences(of: "\"kind\":\"api_promo\"", with: "\"kind\":\"bogus\""),
            json.replacingOccurrences(of: "\"expiresAt\":\(now+86400)", with: "\"expiresAt\":0"),
            json.replacingOccurrences(of: "\"expiresAt\":\(now+86400)", with: "\"expiresAt\":\(now+86400),\"noExpiry\":true")
        ] {
            XCTAssertThrowsError(try DashboardSnapshot.decode(Data(mutation.utf8), now: date))
        }
    }

    @MainActor
    func testBenefitRowLayoutAndOptionalPreview() throws {
        let current = Int64(Date().timeIntervalSince1970)
        let group = BenefitGroup(kind: "api_promo", total: 82.4, unlimited: nil, grants: [
            BenefitGrant(id: "one", label: "API promotional credit", amount: 12.4, expiresAt: current + 86400, noExpiry: nil),
            BenefitGrant(id: "two", label: "API promotional credit", amount: 70, expiresAt: current + 1209600, noExpiry: nil)
        ], note: "Fixture only")
        let benefit = DashboardBenefit(id: "preview", provider: "Claude", product: "Ultra", group: group, updatedAt: current, attemptedAt: current, failure: "", source: "fixture", stale: false)
        let view = VStack(alignment: .leading, spacing: 12) {
            Text("Claude · Ultra — DEMO DATA").font(.headline).padding(.horizontal, 14)
            BenefitRow(benefit: benefit, expandedIDs: .constant([]))
            Divider()
            BenefitRow(benefit: benefit, expandedIDs: .constant(["preview"]))
        }
        .padding(.vertical, 14)
        .frame(width: DashboardView.preferredWidth)
        .background(Color(NSColor.windowBackgroundColor))
        let host = NSHostingView(rootView: view)
        let size = host.fittingSize
        XCTAssertEqual(size.width, DashboardView.preferredWidth, accuracy: 1)
        XCTAssertGreaterThan(size.height, 100)
        XCTAssertLessThan(size.height, 450)
        host.frame = NSRect(origin: .zero, size: size)
        host.layoutSubtreeIfNeeded()
        if let path = ProcessInfo.processInfo.environment["AIUSAGE_BENEFIT_PREVIEW"] {
            let bitmap = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
            host.cacheDisplay(in: host.bounds, to: bitmap)
            let data = try XCTUnwrap(bitmap.representation(using: .png, properties: [:]))
            try data.write(to: URL(fileURLWithPath: path))
        }
    }
}
