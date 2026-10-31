import SwiftUI

// Presentation preferences only. Never filter the collector's snapshot or quotas.
final class RowVisibility: ObservableObject {
    static let preferenceKey = "dashboard.hiddenRows.v1"
    @Published private(set) var hiddenIDs: Set<String>
    @Published private(set) var revealedGroups: Set<String> = []
    private let defaults: UserDefaults

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        hiddenIDs = Set(defaults.stringArray(forKey: Self.preferenceKey) ?? [])
    }

    static func key(_ quota: DashboardQuota) -> String { "quota:" + quota.id }
    static func key(_ benefit: DashboardBenefit) -> String { "benefit:" + benefit.id }

    func hiddenCount(in group: QuotaGroup) -> Int {
        (group.quotas.map(Self.key) + group.benefits.map(Self.key)).filter { hiddenIDs.contains($0) }.count
    }

    func isVisible(_ id: String, in group: QuotaGroup) -> Bool {
        !hiddenIDs.contains(id) || revealedGroups.contains(group.id)
    }

    func setHidden(_ hidden: Bool, id: String, in group: QuotaGroup) {
        if hidden { hiddenIDs.insert(id) } else { hiddenIDs.remove(id) }
        defaults.set(hiddenIDs.sorted(), forKey: Self.preferenceKey)
        if hiddenCount(in: group) == 0 { revealedGroups.remove(group.id) }
    }

    func toggleReveal(_ group: QuotaGroup) {
        guard hiddenCount(in: group) > 0 else { return }
        if revealedGroups.contains(group.id) { revealedGroups.remove(group.id) }
        else { revealedGroups.insert(group.id) }
    }
}

struct HiddenItemsButton: View {
    let count: Int
    let revealed: Bool
    let provider: String
    let action: () -> Void

    private var actionLabel: String {
        revealed ? "Conceal hidden items" : "Show \(count) hidden \(count == 1 ? "item" : "items")"
    }

    var body: some View {
        Button(action: action) {
            HStack(spacing: 4) {
                Image(systemName: revealed ? "eye" : "eye.slash")
                Text("\(count)").monospacedDigit()
            }
            .font(.caption.weight(.medium))
            .foregroundStyle(revealed ? Color.accentColor : Color.secondary)
            .padding(.horizontal, 7)
            .padding(.vertical, 4)
            .background(Capsule().fill(revealed ? Color.accentColor.opacity(0.12) : Color.secondary.opacity(0.09)))
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .help(actionLabel)
        .accessibilityLabel("\(provider): \(actionLabel)")
        .accessibilityValue(revealed ? "Hidden items temporarily visible" : "Hidden items concealed")
        .accessibilityHint("Does not change which items are permanently hidden")
    }
}

struct VisibilityRow<Content: View>: View {
    let hidden: Bool
    let title: String
    let setHidden: (Bool) -> Void
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            content()
                .saturation(hidden ? 0 : 1)
                .opacity(hidden ? 0.8 : 1)
            if hidden {
                HStack(spacing: 6) {
                    Label("Hidden · shown temporarily", systemImage: "eye.slash")
                        .foregroundStyle(.secondary)
                    Spacer(minLength: 8)
                    Button { setHidden(false) } label: {
                        Label("Unhide", systemImage: "eye")
                            .fontWeight(.medium)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Color.accentColor)
                    .help("Keep \(title) visible")
                    .accessibilityLabel("Unhide \(title)")
                }
                .font(.caption2)
                .padding(.horizontal, 24)
                .padding(.top, 2)
                .padding(.bottom, 7)
            }
        }
        .background {
            if hidden {
                RoundedRectangle(cornerRadius: 8, style: .continuous)
                    .fill(Color.secondary.opacity(0.07))
                    .padding(.horizontal, 8)
                    .padding(.vertical, 1)
            }
        }
        .contentShape(Rectangle())
        .contextMenu {
            Button { setHidden(!hidden) } label: {
                Label(hidden ? "Unhide this item" : "Hide this item", systemImage: hidden ? "eye" : "eye.slash")
            }
        }
        .accessibilityAction(named: Text(hidden ? "Unhide \(title)" : "Hide \(title)")) { setHidden(!hidden) }
    }
}
