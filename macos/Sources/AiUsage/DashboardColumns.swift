import SwiftUI

// One compact grid for every provider and both quota and benefit summaries.
// Empty benefit bar cells deliberately preserve the same value/status columns.
enum DashboardColumns {
    static let label: CGFloat = 136
    static let bar: CGFloat = 173
    static let barHeight: CGFloat = 8
    static let value: CGFloat = 44
    static let status: CGFloat = 42
    static let spacing: CGFloat = 3
    static let horizontalPadding: CGFloat = 13
    static let labelInset: CGFloat = 3
    static let rowHeight: CGFloat = 22
    static let width = label + bar + value + status + 3 * spacing + 2 * horizontalPadding
}

struct DashboardRowColumns<Label: View, Bar: View, Value: View, Status: View>: View {
    @ViewBuilder var label: () -> Label
    @ViewBuilder var bar: () -> Bar
    @ViewBuilder var value: () -> Value
    @ViewBuilder var status: () -> Status

    var body: some View {
        HStack(spacing: DashboardColumns.spacing) {
            label()
                .font(.system(size: 11, weight: .medium))
                .lineLimit(1)
                .frame(width: DashboardColumns.label, alignment: .leading)
            bar()
                .frame(width: DashboardColumns.bar)
            value()
                .font(.system(size: 11, weight: .semibold))
                .monospacedDigit()
                .foregroundStyle(.primary)
                .lineLimit(1)
                .minimumScaleFactor(0.85)
                .frame(width: DashboardColumns.value, alignment: .trailing)
            status()
                .font(.system(size: 10))
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .frame(width: DashboardColumns.status, alignment: .trailing)
        }
        .frame(height: DashboardColumns.rowHeight)
    }
}
