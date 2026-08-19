import AppKit
import SwiftUI

struct DashboardView: View {
    static let preferredWidth: CGFloat = 470

    @ObservedObject var store: DashboardStore
    let onSizeChange: (CGSize) -> Void
    @State private var selectedID: String?
    @State private var originatingQuotaID: String?
    @FocusState private var focusedControl: FocusTarget?

    private enum FocusTarget: Hashable {
        case quota(String)
        case back
        case quit
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Color.clear.frame(height: 8)
            ScrollView {
                pageContent
                    .frame(maxWidth: .infinity)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .id(selectedID ?? "dashboard")
            .frame(height: scrollHeight)
        }
        .frame(width: Self.preferredWidth)
        .fixedSize(horizontal: false, vertical: true)
        .background(SizeReader())
        .onPreferenceChange(DashboardSizeKey.self, perform: onSizeChange)
        .onReceive(store.$snapshot) { snapshot in
            if let selectedID, snapshot?.quotas.contains(where: { $0.id == selectedID }) != true {
                self.selectedID = nil
                originatingQuotaID = nil
                focusedControl = nil
            }
        }
    }

    private var scrollHeight: CGFloat {
        if selectedID != nil { return 330 }
        let groupCount = store.groups.count
        let hasSnapshotBanner = store.snapshot.map { $0.state != "ready" && !$0.message.isEmpty } ?? false
        let bannerCount = (store.transportError == nil ? 0 : 1) + (hasSnapshotBanner ? 1 : 0)
        guard groupCount > 0 else { return min(300, CGFloat(144 + bannerCount * 70)) }
        let rowCount = store.groups.reduce(0) { $0 + $1.quotas.count }
        return min(300, CGFloat(groupCount * 34 + rowCount * 31 + max(0, groupCount - 1) * 12 + bannerCount * 70))
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
        VStack(alignment: .leading, spacing: 0) {
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    providerName(group)
                    Spacer(minLength: 8)
                    Text("Updated \(relativeTime(group.quotas.compactMap(\.updatedAt).max()))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize()
                }
                VStack(alignment: .leading, spacing: 2) {
                    providerName(group)
                    Text("Updated \(relativeTime(group.quotas.compactMap(\.updatedAt).max()))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            .padding(.horizontal, 14)
            .padding(.top, 6)
            .padding(.bottom, 8)
            .accessibilityElement(children: .combine)

            ForEach(group.quotas) { quota in
                quotaRow(quota)
            }
        }
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
        return Button { showDetail(quota.id) } label: {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) {
                    Text(quota.window)
                        .font(.subheadline)
                        .lineLimit(1)
                        .padding(.leading, 10)
                        .frame(width: 100, alignment: .leading)
                    QuotaBar(value: quota.remainingPercent, tint: state.color)
                        .frame(width: 120)
                    Text("\(compactPercent(quota.remainingPercent))% left")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.primary)
                        .frame(width: 58, alignment: .trailing)
                    Text(resetText(quota.resetAt))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .frame(width: 112, alignment: .leading)
                    quotaStatusIcon(quota)
                        .frame(width: 8)
                }
                VStack(alignment: .leading, spacing: 3) {
                    HStack {
                        Text(quota.window)
                            .font(.subheadline)
                            .padding(.leading, 10)
                        Spacer()
                        Text("\(compactPercent(quota.remainingPercent))% left")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.primary)
                        quotaStatusIcon(quota)
                    }
                    HStack(spacing: 8) {
                        QuotaBar(value: quota.remainingPercent, tint: state.color)
                        Text(resetText(quota.resetAt))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            .contentShape(Rectangle())
            .padding(.horizontal, 14)
            .padding(.vertical, 4)
        }
        .buttonStyle(.plain)
        .focused($focusedControl, equals: .quota(quota.id))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(quota.provider) \(quota.product), \(quota.window)")
        .accessibilityValue("\(compactPercent(quota.remainingPercent)) percent remaining, \(state.label), \(resetText(quota.resetAt)), \(updateText(quota))")
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
                    Text(quota.window).font(.headline)
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
                    QuotaBar(value: quota.remainingPercent, tint: semanticState(quota.remainingPercent).color)
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
                    .renderingMode(resource == "openai" ? .template : .original)
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
    let value: Double
    let tint: Color

    var body: some View {
        let normalizedValue = normalizedPercent(value)
        GeometryReader { proxy in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2, style: .continuous)
                    .fill(Color.primary.opacity(0.16))
                RoundedRectangle(cornerRadius: 2, style: .continuous)
                    .fill(tint)
                    .frame(width: proxy.size.width * min(max(normalizedValue, 0), 100) / 100)
            }
        }
        .frame(height: 14)
        .accessibilityHidden(true)
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

private func providerIconResource(_ provider: String) -> String? {
    switch provider.lowercased() {
    case let name where name.contains("claude"): return "claude"
    case let name where name.contains("openai") || name.contains("codex"): return "openai"
    case let name where name.contains("kimi"): return "kimi"
    default: return nil
    }
}

private func providerSymbol(_ provider: String) -> String {
    switch provider.lowercased() {
    case let name where name.contains("claude"): return "sparkles"
    case let name where name.contains("openai") || name.contains("codex"): return "chevron.left.forwardslash.chevron.right"
    case let name where name.contains("kimi"): return "moon.stars.fill"
    default: return "cpu"
    }
}

private extension Color {
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

func quotaNeedsAttention(_ quota: DashboardQuota) -> Bool {
    quota.stale || !quota.failure.isEmpty
}

private func normalizedPercent(_ value: Double) -> Double {
    (value * 100).rounded() / 100
}

private func compactPercent(_ value: Double) -> String {
    normalizedPercent(value).formatted(.number.precision(.fractionLength(0...2)))
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
