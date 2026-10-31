import Foundation
import XCTest
@testable import AiUsage

final class CompactFormattingTests: XCTestCase {
    let now: Int64 = 1_793_448_000

    func testPercentagesRoundToWholeNumbersWithoutChangingData() {
        for (value, expected) in [(0.0, "0"), (0.11, "0"), (0.5, "1"), (1.49, "1"), (87.39, "87"), (99.49, "99"), (99.75, "100"), (100.0, "100")] {
            XCTAssertEqual(compactPercent(value), expected)
        }
        XCTAssertNotEqual(quotaMarkerOffset(width: 173, elapsed: 87.39, displayScale: 2), quotaMarkerOffset(width: 173, elapsed: 87, displayScale: 2))
    }

    func testPacingMarkerRemainsVisibleOnRetinaAndNonRetinaDisplays() {
        XCTAssertEqual(DashboardColumns.barHeight - 2 * quotaMarkerVerticalInset(displayScale: 1), 4)
        XCTAssertEqual(DashboardColumns.barHeight - 2 * quotaMarkerVerticalInset(displayScale: 2), 6)
        XCTAssertGreaterThan(DashboardColumns.barHeight - 2 * quotaMarkerVerticalInset(displayScale: 0), 0)
    }

    func testResetSummaryKeepsUnknownDueAndFutureDistinct() {
        let date = Date(timeIntervalSince1970: TimeInterval(now))
        XCTAssertEqual(compactResetText(nil, now: date), "—")
        XCTAssertEqual(compactResetText(now, now: date), "Due")
        XCTAssertEqual(compactResetText(now - 1, now: date), "Due")
        for (seconds, expected) in [(1, "<1m"), (60, "1m"), (3599, "59m"), (3600, "1h 0m"), (8219, "2h 16m"), (86400, "1d"), (86401, "2d")] {
            XCTAssertEqual(compactResetText(now + Int64(seconds), now: date), expected)
        }
    }

    func testCompactBenefitsPreserveUnknownZeroUnitsAndPrecisionInHelp() {
        func group(_ kind: String, _ total: Double?, unlimited: Bool? = nil) -> BenefitGroup {
            BenefitGroup(kind: kind, total: total, unlimited: unlimited, grants: nil, note: nil)
        }
        XCTAssertEqual(group("credits", nil).compactValueText(at: now), "N/A")
        XCTAssertEqual(group("credits", nil, unlimited: true).compactValueText(at: now), "∞")
        XCTAssertEqual(group("credits", 62500).compactValueText(at: now), "62,500")
        XCTAssertEqual(group("credits", 62500).valueText(at: now), "62,500 credits")
        XCTAssertEqual(group("banked_resets", 1).compactValueText(at: now), "1")
        XCTAssertEqual(group("banked_resets", 0).compactValueText(at: now), "0")
        XCTAssertEqual(group("banked_resets", 1).valueText(at: now), "1 available")
        XCTAssertEqual(group("api_promo", 82.4).compactValueText(at: now), "$82.40")
        XCTAssertEqual(group("credits", 0.000001).compactValueText(at: now), "<0.01")
        XCTAssertEqual(group("api_promo", 0.001).compactValueText(at: now), "<$0.01")
        XCTAssertLessThanOrEqual(group("credits", 99999.99).compactValueText(at: now).count, 6)
        XCTAssertLessThanOrEqual(group("credits", 1e12).compactValueText(at: now).count, 5)
        XCTAssertLessThanOrEqual(group("api_promo", 1e12).compactValueText(at: now).count, 6)
    }

    func testCompactExpiryRetainsWarningsAndIncompleteMarker() {
        func group(_ expiry: Int64?, noExpiry: Bool? = nil, total: Double = 1) -> BenefitGroup {
            BenefitGroup(kind: "banked_resets", total: total, unlimited: nil, grants: [
                BenefitGrant(id: "one", label: "Reset", amount: 1, expiresAt: expiry, noExpiry: noExpiry)
            ], note: nil)
        }
        XCTAssertEqual(group(nil).compactExpiry(at: now), BenefitExpiry(text: "—", warning: false))
        XCTAssertEqual(group(nil, noExpiry: true).compactExpiry(at: now), BenefitExpiry(text: "∞", warning: false))
        XCTAssertEqual(group(now).compactExpiry(at: now), BenefitExpiry(text: "Expired", warning: false))
        XCTAssertEqual(group(now).compactValueText(at: now), "0")
        XCTAssertEqual(group(now + 1).compactExpiry(at: now), BenefitExpiry(text: "<1m", warning: true))
        XCTAssertEqual(group(now + 86400).compactExpiry(at: now), BenefitExpiry(text: "1d", warning: true))
        XCTAssertEqual(group(now + 172800, total: 3).compactExpiry(at: now), BenefitExpiry(text: "2d*", warning: true))
        XCTAssertTrue(group(now + 172800, total: 3).expiry(at: now).text.hasPrefix("Next known"))
        let distant = group(now + 172801, total: 3).compactExpiry(at: now)
        XCTAssertFalse(distant.warning)
        XCTAssertTrue(distant.text.hasSuffix("*"))
        XCTAssertFalse(distant.text.contains("Expires"))
    }
}
