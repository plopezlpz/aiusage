import AppKit
import XCTest
@testable import AiUsage

final class ProviderIconTests: XCTestCase {
    @MainActor
    func testAntigravityBrandedIconLoadsAndRenders() throws {
        XCTAssertEqual(providerIconResource("Antigravity"), "antigravity")
        let resources = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Resources/ProviderIcons/antigravity.svg")
        let image = try XCTUnwrap(NSImage(contentsOf: resources))
        let bitmap = try XCTUnwrap(NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 96, pixelsHigh: 96,
            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
            colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0))
        let context = try XCTUnwrap(NSGraphicsContext(bitmapImageRep: bitmap))
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = context
        image.draw(in: NSRect(x: 0, y: 0, width: 96, height: 96))
        NSGraphicsContext.restoreGraphicsState()
        let visiblePixels = (0..<96).reduce(0) { total, y in
            total + (0..<96).filter { (bitmap.colorAt(x: $0, y: y)?.alphaComponent ?? 0) > 0.1 }.count
        }
        XCTAssertGreaterThan(visiblePixels, 500, "SVG must render a visible logo, not merely decode")
        if let path = ProcessInfo.processInfo.environment["AIUSAGE_ICON_PREVIEW"] {
            try XCTUnwrap(bitmap.representation(using: .png, properties: [:])).write(to: URL(fileURLWithPath: path))
        }
    }
}
