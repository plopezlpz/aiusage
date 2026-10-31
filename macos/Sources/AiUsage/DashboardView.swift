import AppKit
import SwiftUI

struct DashboardView: View {
    static let preferredWidth: CGFloat = DashboardColumns.width

    @ObservedObject var store: DashboardStore
    let onSizeChange: (CGSize) -> Void
    @State private var selectedID: String?
    @State private var originatingQuotaID: String?
    @State private var expandedBenefitIDs: Set<String> = []
    @StateObject private var visibility: RowVisibility

    init(store: DashboardStore, onSizeChange: @escaping (CGSize) -> Void, visibility: RowVisibility = RowVisibility()) {
        self.store = store
        self.onSizeChange = onSizeChange
        _visibility = StateObject(wrappedValue: visibility)
    }

    @FocusState private var focusedControl: FocusTarget?

    private enum FocusTarget: Hashable {
        case quota(String)
        case hiddenItems(String)
        case back
        case quit
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Color.clear.frame(height: 8)
            page
        }
        .frame(width: Self.preferredWidth)
        .fixedSize(horizontal: false, vertical: true)
        .background(SizeReader())
        .onPreferenceChange(DashboardSizeKey.self, perform: onSizeChange)
        .onReceive(store.$snapshot) { snapshot in
            // Stable IDs preserve disclosure state across refreshes; prune rows that disappear.
            expandedBenefitIDs.formIntersection(Set((snapshot?.benefits ?? []).map(\.id)))
            if let selectedID, snapshot?.quotas.contains(where: { $0.id == selectedID }) != true {
                self.selectedID = nil
                originatingQuotaID = nil
                focusedControl = nil
            }
        }
    }

    @ViewBuilder private var page: some View {
        let visibleScreenHeight = NSScreen.screens.map(\.visibleFrame.height).min() ?? 700
        let maximumHeight = dashboardMaximumPageHeight(visibleScreenHeight: visibleScreenHeight)
        ContentFittingScrollView(maximumHeight: maximumHeight) {
            paddedPageContent
        }
        .id(selectedID)
    }

    private var paddedPageContent: some View {
        pageContent
            .frame(maxWidth: .infinity)
            .padding(.bottom, 16)
            .fixedSize(horizontal: false, vertical: true)
    }

    @ViewBuilder private var pageContent: some View {
        if let selectedID, let quota = store.snapshot?.quotas.first(where: { $0.id == selectedID }) {
            detail(quota)
        } else {
            dashboard
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 1) {
            HStack(spacing: 8) {
                Image(systemName: "chart.bar.xaxis")
                    .foregroundStyle(.secondary)
                    .frame(width: 18)
                    .accessibilityHidden(true)
                Text("AI Usage")
                    .font(.headline)
                Spacer(minLength: 8)
                Button("Quit") { NSApp.terminate(nil) }
                    .buttonStyle(HeaderButtonStyle())
                    .focused($focusedControl, equals: .quit)
                    .keyboardShortcut("q", modifiers: .command)
                    .help("Quit AI Usage")
                    .accessibilityLabel("Quit AI Usage")
                Button { store.request(.force) } label: {
                    ZStack {
                        Image(systemName: "arrow.clockwise")
                            .opacity(store.isRefreshing ? 0 : 1)
                        ProgressView()
                            .controlSize(.small)
                            .opacity(store.isRefreshing ? 1 : 0)
                    }
                    .frame(width: 16, height: 16)
                }
                .buttonStyle(HeaderButtonStyle())
                .keyboardShortcut("r", modifiers: .command)
                .help("Refresh all providers")
                .accessibilityLabel(store.isRefreshing ? "Refreshing usage" : "Refresh usage")
                .accessibilityHint("Refreshes every provider, bypassing recent cached attempts")
            }
            Text(store.isRefreshing ? refreshStatus : "Refresh status")
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(.leading, 26)
                .opacity(store.isRefreshing ? 1 : 0)
                .accessibilityHidden(!store.isRefreshing)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }

    private var dashboard: some View {
        VStack(spacing: 0) {
            banners
            if store.groups.isEmpty {
                emptyState
            } else {
                ForEach(Array(store.groups.enumerated()), id: \.element.id) { index, group in
                    if index > 0 { Color.clear.frame(height: 12) }
                    provider(group)
                }
            }
        }
    }

    @ViewBuilder private var banners: some View {
        if let error = store.transportError {
            Banner(symbol: "exclamationmark.triangle", title: "Couldn’t update", message: error)
                .padding(.horizontal, 14)
                .padding(.top, 12)
        }
        if let snapshot = store.snapshot, snapshot.state != "ready", !snapshot.message.isEmpty {
            Banner(
                symbol: snapshot.state == "unavailable" ? "wifi.slash" : "exclamationmark.circle",
                title: snapshot.state == "unavailable" ? "Usage unavailable" : "Some providers need attention",
                message: snapshot.message
            )
            .padding(.horizontal, 14)
            .padding(.top, 12)
        }
    }

    private var emptyState: some View {
        VStack(spacing: 8) {
            Image(systemName: store.isRefreshing ? "arrow.triangle.2.circlepath" : "chart.bar.xaxis")
                .font(.system(size: 24))
                .foregroundStyle(.secondary)
            Text(store.isRefreshing ? "Loading usage…" : "No usage data yet")
                .font(.headline)
            Text("Open a provider CLI and refresh, or connect Claude Code’s status line.")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .padding(.horizontal, 28)
        .padding(.vertical, 24)
        .accessibilityElement(children: .combine)
    }

    private func provider(_ group: QuotaGroup) -> some View {
        let quotas = group.quotas.filter { visibility.isVisible(RowVisibility.key($0), in: group) }
        let benefits = group.benefits.filter { visibility.isVisible(RowVisibility.key($0), in: group) }
        let hiddenCount = visibility.hiddenCount(in: group)
        return VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .center, spacing: 8) {
                ViewThatFits(in: .horizontal) {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        providerName(group)
                        Spacer(minLength: 8)
                        providerUpdate(group)
                            .fixedSize()
                    }
                    VStack(alignment: .leading, spacing: 2) {
                        providerName(group)
                        providerUpdate(group)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                if hiddenCount > 0 {
                    HiddenItemsButton(count: hiddenCount, revealed: visibility.revealedGroups.contains(group.id), provider: group.provider) {
                        visibility.toggleReveal(group)
                    }
                    .fixedSize()
                    .focused($focusedControl, equals: .hiddenItems(group.id))
                }
            }
            .padding(.horizontal, 14)
            .padding(.top, 6)
            .padding(.bottom, 8)
            .accessibilityElement(children: .contain)

            ForEach(quotas) { quota in
                visibilityRow(id: RowVisibility.key(quota), title: quotaWindowLabel(quota), group: group) {
                    quotaRow(quota)
                }
            }
            if !benefits.isEmpty {
                Color.clear.frame(height: 3)
                ForEach(benefits) { benefit in
                    visibilityRow(id: RowVisibility.key(benefit), title: benefit.group.title, group: group) {
                        BenefitRow(benefit: benefit, expandedIDs: $expandedBenefitIDs)
                    }
                }
            }
            if quotas.isEmpty && benefits.isEmpty && hiddenCount > 0 {
                Text("All items hidden")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 24)
                    .padding(.bottom, 6)
            }
        }
    }

    private func providerUpdate(_ group: QuotaGroup) -> some View {
        Text("Updated \(relativeTime(group.quotas.compactMap(\.updatedAt).max()))")
            .font(.caption)
            .foregroundStyle(.secondary)
    }

    private func visibilityRow<Content: View>(id: String, title: String, group: QuotaGroup, @ViewBuilder content: @escaping () -> Content) -> some View {
        VisibilityRow(hidden: visibility.hiddenIDs.contains(id), title: title, setHidden: { hidden in
            visibility.setHidden(hidden, id: id, in: group)
            if hidden && !visibility.revealedGroups.contains(group.id) {
                focusedControl = .hiddenItems(group.id)
            }
        }, content: content)
    }

    private func providerName(_ group: QuotaGroup) -> some View {
        HStack(alignment: .center, spacing: 6) {
            ProviderIcon(provider: group.provider)
            Text(group.provider).fontWeight(.semibold)
            Text(group.product).foregroundStyle(.secondary)
        }
        .font(.subheadline)
    }

    private func quotaRow(_ quota: DashboardQuota) -> some View {
        let state = semanticState(quota.remainingPercent)
        let elapsedAccessibility = quota.elapsedPercent.map { ", \(compactPercent($0)) percent of window elapsed" } ?? ""
        return Button { showDetail(quota.id) } label: {
            DashboardRowColumns {
                Text(quotaWindowLabel(quota))
                    .padding(.leading, DashboardColumns.labelInset)
                    .help(quotaWindowLabel(quota))
            } bar: {
                QuotaBar(value: quota.remainingPercent, elapsed: quota.elapsedPercent, tint: state.color)
            } value: {
                Text("\(compactPercent(quota.remainingPercent))%")
            } status: {
                Text(compactResetText(quota.resetAt))
                    .help("\(resetText(quota.resetAt))\n\(exactDate(quota.resetAt))")
            }
            .overlay(alignment: .leading) {
                if quotaNeedsAttention(quota) {
                    quotaStatusIcon(quota)
                        .frame(width: 10)
                        .offset(x: -DashboardColumns.horizontalPadding)
                }
            }
            .contentShape(Rectangle())
            .padding(.horizontal, DashboardColumns.horizontalPadding)
        }
        .buttonStyle(.plain)
        .focused($focusedControl, equals: .quota(quota.id))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(quota.provider) \(quota.product), \(quotaWindowLabel(quota))")
        .accessibilityValue("\(compactPercent(quota.remainingPercent)) percent remaining, \(state.label)\(elapsedAccessibility), \(resetText(quota.resetAt)), \(updateText(quota))")
        .accessibilityHint("Shows quota details")
    }

    private func detail(_ quota: DashboardQuota) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Button { showDashboard() } label: {
                    Label("Back", systemImage: "chevron.left")
                }
                .buttonStyle(.plain)
                .focused($focusedControl, equals: .back)
                .help("Back to quotas")
                Spacer()
                Text("\(quota.provider) · \(quota.product)")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 10)

            Divider().padding(.horizontal, 16)

            VStack(alignment: .leading, spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(quotaWindowLabel(quota)).font(.headline)
                    ViewThatFits(in: .horizontal) {
                        HStack(alignment: .firstTextBaseline) {
                            Text("\(compactPercent(quota.remainingPercent))% remaining")
                                .font(.title3.weight(.bold))
                                .foregroundStyle(.primary)
                                .fixedSize()
                            Spacer()
                            Text("\(compactPercent(100 - quota.remainingPercent))% used")
                                .foregroundStyle(.secondary)
                                .fixedSize()
                        }
                        VStack(alignment: .leading, spacing: 2) {
                            Text("\(compactPercent(quota.remainingPercent))% remaining")
                                .font(.title3.weight(.bold))
                                .foregroundStyle(.primary)
                            Text("\(compactPercent(100 - quota.remainingPercent))% used")
                                .foregroundStyle(.secondary)
                        }
                    }
                    QuotaBar(value: quota.remainingPercent, elapsed: quota.elapsedPercent, tint: semanticState(quota.remainingPercent).color)
                        .frame(height: 14)
                }

                detailLine("Reset", value: "\(resetText(quota.resetAt))\n\(exactDate(quota.resetAt))")
                detailLine("Last success", value: exactDate(quota.updatedAt))
                detailLine("Last attempt", value: exactDate(quota.attemptedAt))
                if !quota.failure.isEmpty {
                    detailLine("Refresh failure", value: quota.failure)
                }
                detailLine("Source", value: quota.source.isEmpty ? "Not reported" : quota.source)
                if !quota.detail.isEmpty {
                    detailLine("Detail", value: quota.detail)
                }
                if quota.stale {
                    Label("Values are stale", systemImage: "clock.badge.exclamationmark")
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.secondary)
                }
            }
            .padding(16)
        }
    }

    private func showDetail(_ quotaID: String) {
        originatingQuotaID = quotaID
        selectedID = quotaID
        DispatchQueue.main.async { focusedControl = .back }
    }

    private func showDashboard() {
        let quotaID = originatingQuotaID
        selectedID = nil
        DispatchQueue.main.async {
            focusedControl = quotaID.map(FocusTarget.quota)
        }
    }

    @ViewBuilder private func quotaStatusIcon(_ quota: DashboardQuota) -> some View {
        if quotaNeedsAttention(quota) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.caption2.weight(.semibold))
                .foregroundStyle(Color.usageOrange)
                .help(quota.failure.isEmpty ? updateText(quota) : "\(updateText(quota)): \(quota.failure)")
        } else {
            Image(systemName: "chevron.right")
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.tertiary)
        }
    }

    private func detailLine(_ label: String, value: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label.uppercased())
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.secondary)
            Text(value)
                .font(.subheadline)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
        }
        .accessibilityElement(children: .combine)
    }

    private var refreshStatus: String {
        switch store.refreshKind {
        case .force: return "Refreshing all providers…"
        case .auto: return "Checking providers…"
        default: return "Reading latest usage…"
        }
    }
}

private struct HeaderButtonStyle: ButtonStyle {
    @State private var isHovered = false

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .padding(.horizontal, 6)
            .frame(height: 24)
            .background {
                RoundedRectangle(cornerRadius: 5, style: .continuous)
                    .fill(Color.primary.opacity(configuration.isPressed ? 0.16 : isHovered ? 0.08 : 0))
            }
            .contentShape(RoundedRectangle(cornerRadius: 5, style: .continuous))
            .onHover { isHovered = $0 }
            .animation(.easeOut(duration: 0.08), value: isHovered)
    }
}

private struct ProviderIcon: View {
    let provider: String

    var body: some View {
        let resource = providerIconResource(provider)
        ZStack {
            if resource == "kimi" {
                RoundedRectangle(cornerRadius: 4, style: .continuous).fill(.black)
            }
            if let resource,
               let url = Bundle.main.url(forResource: resource, withExtension: "svg", subdirectory: "ProviderIcons"),
               let image = NSImage(contentsOf: url) {
                Image(nsImage: image)
                    .renderingMode(resource == "openai" || resource == "zai" ? .template : .original)
                    .resizable()
                    .scaledToFit()
                    .foregroundStyle(.primary)
                    .padding(resource == "kimi" ? 2 : 0)
            } else {
                Image(systemName: providerSymbol(provider))
                    .foregroundStyle(.secondary)
            }
        }
        .frame(width: 18, height: 18)
        .accessibilityHidden(true)
    }
}

private struct QuotaBar: View {
    @Environment(\.displayScale) private var displayScale

    let value: Double
    let elapsed: Double?
    let tint: Color

    var body: some View {
        let normalizedValue = normalizedPercent(value)
        let markerWidth = 2 / max(displayScale, 1)
        GeometryReader { proxy in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2, style: .continuous)
                    .fill(Color.primary.opacity(0.16))
                RoundedRectangle(cornerRadius: 2, style: .continuous)
                    .fill(tint)
                    .frame(width: proxy.size.width * min(max(normalizedValue, 0), 100) / 100)
                if let elapsed {
                    Rectangle()
                        .fill(Color.white.opacity(0.75))
                        .frame(width: markerWidth)
                        .blendMode(.difference)
                        .padding(.vertical, quotaMarkerVerticalInset(displayScale: displayScale))
                        .offset(x: quotaMarkerOffset(width: proxy.size.width, elapsed: elapsed, displayScale: displayScale))
                }
            }
            .clipShape(RoundedRectangle(cornerRadius: 2, style: .continuous))
        }
        .frame(height: DashboardColumns.barHeight)
        .accessibilityElement()
        .accessibilityLabel("Quota window pacing")
        .accessibilityValue(elapsed.map { "\(compactPercent($0)) percent elapsed" } ?? "")
        .accessibilityHidden(elapsed == nil)
    }
}

private struct Banner: View {
    let symbol: String
    let title: String
    let message: String

    var body: some View {
        HStack(alignment: .top, spacing: 9) {
            Image(systemName: symbol).foregroundStyle(.secondary).accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.caption.weight(.semibold))
                Text(message).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
    }
}

private struct DashboardSizeKey: PreferenceKey {
    static var defaultValue: CGSize = .zero
    static func reduce(value: inout CGSize, nextValue: () -> CGSize) { value = nextValue() }
}

private struct SizeReader: View {
    var body: some View {
        GeometryReader { proxy in Color.clear.preference(key: DashboardSizeKey.self, value: proxy.size) }
    }
}

// Measure the intrinsic page, not the constrained scroll viewport. This also
// handles wrapped labels, error banners, and expanded grants without estimates.
struct ContentFittingScrollView<Content: View>: View {
    let maximumHeight: CGFloat
    @ViewBuilder var content: () -> Content
    @State private var contentHeight: CGFloat = 0

    @ViewBuilder var body: some View {
        if contentHeight > maximumHeight {
            ScrollView { measuredContent }
                .frame(height: maximumHeight)
        } else {
            // Start with intrinsic layout, never a tiny scroll viewport: AppKit
            // must be able to discover the full content before resizing its panel.
            measuredContent
        }
    }

    private var measuredContent: some View {
        content()
            .frame(maxWidth: .infinity)
            .fixedSize(horizontal: false, vertical: true)
            .background(GeometryReader { proxy in
                Color.clear
                    .onAppear { measure(proxy.size.height) }
                    .onChange(of: proxy.size.height) { measure($0) }
            })
    }

    private func measure(_ height: CGFloat) {
        guard height.isFinite, height > 0 else { return }
        contentHeight = ceil(height)
    }
}

func dashboardMaximumPageHeight(visibleScreenHeight: CGFloat) -> CGFloat {
    max(100, visibleScreenHeight - 96)
}

func providerIconResource(_ provider: String) -> String? {
    switch provider.lowercased() {
    case let name where name.contains("claude"): return "claude"
    case let name where name.contains("openai") || name.contains("codex"): return "openai"
    case let name where name.contains("antigravity"): return "antigravity"
    case let name where name.contains("kimi"): return "kimi"
    case let name where name.contains("z.ai") || name.contains("zhipu"): return "zai"
    default: return nil
    }
}

private func providerSymbol(_ provider: String) -> String {
    switch provider.lowercased() {
    case let name where name.contains("claude"): return "sparkles"
    case let name where name.contains("openai") || name.contains("codex"): return "chevron.left.forwardslash.chevron.right"
    case let name where name.contains("antigravity"): return "arrow.up.forward.circle"
    case let name where name.contains("kimi"): return "moon.stars.fill"
    case let name where name.contains("z.ai") || name.contains("zhipu"): return "circle.hexagongrid.fill"
    default: return "cpu"
    }
}

extension Color {
    static let usageGreen = Color(red: 192 / 255, green: 207 / 255, blue: 144 / 255)
    static let usageOrange = Color(red: 255 / 255, green: 159 / 255, blue: 10 / 255)
    static let usageRed = Color(red: 203 / 255, green: 65 / 255, blue: 72 / 255)
}

private func semanticState(_ value: Double) -> (label: String, color: Color) {
    let remaining = normalizedPercent(value)
    if remaining <= 0 { return ("Exhausted", .usageRed) }
    if remaining <= 25 { return ("Low", .usageOrange) }
    return ("Healthy", .usageGreen)
}

func quotaWindowLabel(_ quota: DashboardQuota) -> String {
    guard quota.provider == "Antigravity", antigravityWindowOrder(quota.window) < 2,
          let separator = quota.window.range(of: " · ", options: .backwards) else { return quota.window }
    return String(quota.window[separator.upperBound...]) + " · " + String(quota.window[..<separator.lowerBound])
}

func quotaNeedsAttention(_ quota: DashboardQuota) -> Bool {
    quota.stale || !quota.failure.isEmpty
}

func quotaMarkerVerticalInset(displayScale: CGFloat) -> CGFloat {
    2 / max(displayScale, 1)
}

func quotaMarkerOffset(width: CGFloat, elapsed: Double, displayScale: CGFloat) -> CGFloat {
    let scale = max(displayScale, 1)
    let markerWidth = 2 / scale
    let remainingFraction = CGFloat((100 - min(max(elapsed, 0), 100)) / 100)
    let rawPixelOffset = width * scale * remainingFraction - markerWidth * scale / 2
    let maximumPixelOffset = (max(0, width - markerWidth) * scale).rounded(.down)
    return min(max(rawPixelOffset.rounded(), 0), maximumPixelOffset) / scale
}

private func normalizedPercent(_ value: Double) -> Double {
    (value * 100).rounded() / 100
}

func compactPercent(_ value: Double) -> String {
    value.rounded().formatted(.number.precision(.fractionLength(0)))
}

func compactResetText(_ timestamp: Int64?, now: Date = Date()) -> String {
    guard let timestamp else { return "—" }
    let interval = Date(timeIntervalSince1970: TimeInterval(timestamp)).timeIntervalSince(now)
    if interval <= 0 { return "Due" }
    if interval < 60 { return "<1m" }
    if interval < 3600 { return "\(Int(interval / 60))m" }
    if interval < 86400 { return "\(Int(interval / 3600))h \(Int(interval.truncatingRemainder(dividingBy: 3600) / 60))m" }
    return "\(Int(ceil(interval / 86400)))d"
}

private func resetText(_ timestamp: Int64?) -> String {
    guard let timestamp else { return "Reset unknown" }
    let interval = Date(timeIntervalSince1970: TimeInterval(timestamp)).timeIntervalSinceNow
    if interval <= 0 { return "Reset due" }
    if interval < 60 { return "Resets in <1m" }
    if interval < 3600 { return "Resets in \(Int(interval / 60))m" }
    if interval < 86400 { return "Resets in \(Int(interval / 3600))h \(Int(interval.truncatingRemainder(dividingBy: 3600) / 60))m" }
    return "Resets in \(Int(ceil(interval / 86400)))d"
}

func updateText(_ quota: DashboardQuota) -> String {
    if quota.stale, !quota.failure.isEmpty {
        return "Stale · refresh failed \(relativeTime(quota.attemptedAt))"
    }
    if !quota.failure.isEmpty {
        return "Refresh failed \(relativeTime(quota.attemptedAt))"
    }
    if quota.stale { return "Stale · updated \(relativeTime(quota.updatedAt))" }
    return "Updated \(relativeTime(quota.updatedAt))"
}

private func relativeTime(_ timestamp: Int64?) -> String {
    guard let timestamp else { return "unknown" }
    let seconds = max(0, Int(Date().timeIntervalSince1970) - Int(timestamp))
    if seconds < 60 { return "just now" }
    if seconds < 3600 { return "\(seconds / 60)m ago" }
    if seconds < 86400 { return "\(seconds / 3600)h ago" }
    return "\(seconds / 86400)d ago"
}

private func exactDate(_ timestamp: Int64?) -> String {
    guard let timestamp else { return "Unknown" }
    return Date(timeIntervalSince1970: TimeInterval(timestamp)).formatted(date: .abbreviated, time: .standard)
}
