import AppKit
import SwiftUI
import XCTest
@testable import AiUsage

final class DashboardColumnsTests: XCTestCase {
    @MainActor
    func testSharedColumnsAlignBarsValuesAndStatusWithoutWrapping() throws {
        var frames: [String: CGRect] = [:]
        func measured(_ key: String) -> some View {
            GeometryReader { proxy in
                Color.clear.onAppear { frames[key] = proxy.frame(in: .named("columns")) }
            }
        }
        let values = ["100%", "99%", "N/A", "62,500", "1", "87%", "$82.40", "∞"]
        let labels = ["5-hour session", "5-hour · Claude + GPT", "API promo credits", "Credits", "Banked resets", "Weekly · Gemini 4 Argon", "API promo credits", "Credits"]
        let root = VStack(spacing: 10) {
            ForEach(values.indices, id: \.self) { index in
                DashboardRowColumns {
                    Text(labels[index]).padding(.leading, DashboardColumns.labelInset).background(measured("label\(index)"))
                } bar: {
                    Rectangle().fill(index < 2 ? Color.usageGreen : Color.clear)
                        .frame(height: 8).background(measured("bar\(index)"))
                } value: {
                    Text(values[index]).background(measured("value\(index)"))
                } status: {
                    Text(index < 2 ? "2h 17m" : "Nov 7*").background(measured("status\(index)"))
                }
                .background(measured("row\(index)"))
            }
        }
        .padding(.horizontal, DashboardColumns.horizontalPadding)
        .padding(.vertical, 16)
        .frame(width: DashboardView.preferredWidth)
        .coordinateSpace(name: "columns")
        .background(Color(NSColor.windowBackgroundColor))
        let host = NSHostingView(rootView: root)
        host.frame = NSRect(origin: .zero, size: host.fittingSize)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        XCTAssertEqual(DashboardColumns.width, 430)
        XCTAssertEqual(DashboardColumns.bar, 173)
        XCTAssertEqual(host.fittingSize.width, DashboardColumns.width, accuracy: 0.5)
        let bar = try XCTUnwrap(frames["bar0"])
        let value = try XCTUnwrap(frames["value0"])
        let status = try XCTUnwrap(frames["status0"])
        for index in values.indices {
            let currentRow = try XCTUnwrap(frames["row\(index)"])
            XCTAssertEqual(currentRow.height, DashboardColumns.rowHeight, accuracy: 0.5)
            let currentLabel = try XCTUnwrap(frames["label\(index)"])
            XCTAssertLessThanOrEqual(currentLabel.height, DashboardColumns.rowHeight)
            let naturalLabelWidth = (labels[index] as NSString).size(withAttributes: [.font: NSFont.systemFont(ofSize: 11, weight: .medium)]).width
            XCTAssertGreaterThanOrEqual(currentLabel.width + 0.5, naturalLabelWidth + DashboardColumns.labelInset, "Full label must fit")
            let currentBar = try XCTUnwrap(frames["bar\(index)"])
            let currentValue = try XCTUnwrap(frames["value\(index)"])
            let currentStatus = try XCTUnwrap(frames["status\(index)"])
            XCTAssertEqual(currentBar.minX, bar.minX, accuracy: 0.5)
            XCTAssertEqual(currentBar.width, DashboardColumns.bar, accuracy: 0.5)
            XCTAssertEqual(currentValue.maxX, value.maxX, accuracy: 0.5)
            XCTAssertEqual(currentValue.height, value.height, accuracy: 0.5, "Value must remain on one line")
            XCTAssertEqual(currentStatus.maxX, status.maxX, accuracy: 0.5)
            XCTAssertLessThanOrEqual(currentValue.width, DashboardColumns.value)
            XCTAssertLessThanOrEqual(currentStatus.width, DashboardColumns.status)
        }
        if let path = ProcessInfo.processInfo.environment["AIUSAGE_COLUMNS_PREVIEW"] {
            let bitmap = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
            host.cacheDisplay(in: host.bounds, to: bitmap)
            try XCTUnwrap(bitmap.representation(using: .png, properties: [:])).write(to: URL(fileURLWithPath: path))
        }
    }
}
