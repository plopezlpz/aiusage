import Foundation
import SwiftUI

struct BenefitGrant: Decodable, Equatable, Identifiable {
    let id: String
    let label: String
    let amount: Double
    let expiresAt: Int64?
    let noExpiry: Bool?

    func expired(at now: Int64) -> Bool { expiresAt.map { $0 <= now } ?? false }
}

struct BenefitGroup: Decodable, Equatable {
    let kind: String
    let total: Double?
    let unlimited: Bool?
    let grants: [BenefitGrant]?
    let note: String?

    var title: String {
        switch kind {
        case "api_promo": return "API promo credits"
        case "limit_resets": return "Limit resets"
        case "banked_resets": return "Banked resets"
        default: return "Credits"
        }
    }

    var sortedGrants: [BenefitGrant] {
        (grants ?? []).enumerated().sorted {
            let a = $0.element.expiresAt ?? Int64.max
            let b = $1.element.expiresAt ?? Int64.max
            return a == b ? $0.offset < $1.offset : a < b
        }.map(\.element)
    }

    var missingAmount: Double {
        guard let total else { return 0 }
        return max(0, total - (grants ?? []).reduce(0) { $0 + $1.amount })
    }

    var missingText: String {
        let resetAmount = missingAmount == 1 ? "1 reset" : "\(Int64(missingAmount)) resets"
        let amount = kind == "banked_resets" || kind == "limit_resets" ? resetAmount : amountText(missingAmount)
        return "Expiration details unavailable for " + amount
    }

    var expandable: Bool {
        let count = grants?.count ?? 0
        return count > 1 || count > 0 && missingAmount > 0
    }

    func amountText(_ amount: Double) -> String {
        switch kind {
        case "api_promo": return "$" + amount.formatted(.number.precision(.fractionLength(2)))
        case "limit_resets", "banked_resets": return "\(Int64(amount)) available"
        default: return "\(amount.formatted(.number.precision(.fractionLength(0...6)))) credits"
        }
    }

    func available(at now: Int64) -> Double? {
        total.map { max(0, $0 - (grants ?? []).filter { $0.expired(at: now) }.reduce(0) { $0 + $1.amount }) }
    }

    func valueText(at now: Int64) -> String {
        if unlimited == true { return "Unlimited" }
        return available(at: now).map(amountText) ?? "Not reported"
    }

    // Summary-only abbreviations. Full amounts/units remain in details and help.
    func compactValueText(at now: Int64) -> String {
        if unlimited == true { return "∞" }
        guard let amount = available(at: now) else { return "N/A" }
        let currency = kind == "api_promo"
        if amount > 0, amount < 0.01 { return currency ? "<$0.01" : "<0.01" }
        let prefix = currency ? "$" : ""
        if amount >= (currency ? 1000 : 100_000) || amount >= 1000 && amount != amount.rounded() {
            return prefix + amount.formatted(.number.notation(.compactName).precision(.fractionLength(0...1)))
        }
        if currency { return prefix + amount.formatted(.number.precision(.fractionLength(2))) }
        return amount.formatted(.number.precision(.fractionLength(0...2)))
    }

    func compactExpiry(at now: Int64) -> BenefitExpiry {
        let full = expiry(at: now)
        let active = (grants ?? []).filter { !$0.expired(at: now) }
        guard let nearest = active.compactMap(\.expiresAt).min() else {
            let text = full.text == "No expiration" ? "∞" : full.text == "Expired" ? "Expired" : "—"
            return BenefitExpiry(text: text, warning: full.warning)
        }
        let seconds = nearest - now
        let text: String
        if seconds < 60 { text = "<1m" }
        else if seconds < 3600 { text = "\(Int(ceil(Double(seconds) / 60)))m" }
        else if seconds < 86400 { text = "\(Int(ceil(Double(seconds) / 3600)))h" }
        else if seconds <= 172800 { text = "\(Int(ceil(Double(seconds) / 86400)))d" }
        else { text = Date(timeIntervalSince1970: TimeInterval(nearest)).formatted(.dateTime.month(.abbreviated).day()) }
        // Asterisk preserves the distinction between nearest and nearest *known*.
        let incomplete = missingAmount > 0 || active.contains { $0.expiresAt == nil && $0.noExpiry != true }
        return BenefitExpiry(text: text + (incomplete ? "*" : ""), warning: full.warning)
    }

    func expiry(at now: Int64) -> BenefitExpiry {
        let entries = grants ?? []
        let active = entries.filter { !$0.expired(at: now) }
        if let nearest = active.compactMap(\.expiresAt).min() {
            let value = benefitExpiry(nearest, now: now)
            let unknown = missingAmount > 0 || active.contains { $0.expiresAt == nil && $0.noExpiry != true }
            let prefix = unknown ? "Next known: " : entries.count > 1 ? "Next " : ""
            let text = prefix.isEmpty ? value.text : prefix + value.text.prefix(1).lowercased() + value.text.dropFirst()
            return BenefitExpiry(text: text, warning: value.warning)
        }
        if !active.isEmpty, active.allSatisfy({ $0.noExpiry == true }), missingAmount == 0 {
            return BenefitExpiry(text: "No expiration", warning: false)
        }
        if !entries.isEmpty, active.isEmpty, missingAmount == 0 {
            return BenefitExpiry(text: "Expired", warning: false)
        }
        return BenefitExpiry(text: "Expiry unknown", warning: false)
    }
}

struct DashboardBenefit: Decodable, Equatable, Identifiable {
    let id: String
    let provider: String
    let product: String
    let group: BenefitGroup
    let updatedAt: Int64?
    let attemptedAt: Int64?
    let failure: String
    let source: String
    let stale: Bool

    func validate(generatedAt: Int64) throws {
        func validText(_ value: String, max: Int) -> Bool {
            value.utf8.count <= max && value == value.trimmingCharacters(in: .whitespacesAndNewlines) && !value.unicodeScalars.contains { CharacterSet.controlCharacters.contains($0) || CharacterSet.illegalCharacters.contains($0) || $0.properties.generalCategory == .format }
        }
        let reset = group.kind == "banked_resets" || group.kind == "limit_resets"
        func validAmount(_ n: Double) -> Bool { n.isFinite && n >= 0 && n <= 1e12 && (!reset || n.rounded(.down) == n) }
        guard !id.isEmpty, !provider.isEmpty, !product.isEmpty,
              ["api_promo", "limit_resets", "banked_resets", "credits"].contains(group.kind),
              validText(group.note ?? "", max: 512), (group.grants?.count ?? 0) <= 1000 else {
            throw DashboardDataError.invalid("Invalid benefit group.")
        }
        if let total = group.total, !validAmount(total) { throw DashboardDataError.invalid("Invalid benefit balance.") }
        if group.unlimited == true, group.total != nil || !(group.grants ?? []).isEmpty {
            throw DashboardDataError.invalid("Conflicting unlimited benefit.")
        }
        for timestamp in [updatedAt, attemptedAt].compactMap({ $0 }) {
            guard timestamp >= 1_577_836_800, timestamp <= generatedAt + 300 else { throw DashboardDataError.invalid("Invalid benefit timestamp.") }
        }
        var ids = Set<String>()
        var sum = 0.0
        for grant in group.grants ?? [] {
            guard !grant.id.isEmpty, validText(grant.id, max: 256), ids.insert(grant.id).inserted,
                  validText(grant.label, max: 128), validAmount(grant.amount), grant.amount > 0 else {
                throw DashboardDataError.invalid("Invalid benefit grant.")
            }
            if let expires = grant.expiresAt {
                guard expires >= 1_577_836_800, expires <= 7_258_118_400, grant.noExpiry != true else { throw DashboardDataError.invalid("Invalid benefit expiration.") }
            }
            sum += grant.amount
        }
        if !(group.grants ?? []).isEmpty, group.total == nil || sum > (group.total ?? 0) + 0.000001 {
            throw DashboardDataError.invalid("Benefit grants exceed balance.")
        }
    }
}

struct BenefitExpiry: Equatable {
    let text: String
    let warning: Bool
}

func benefitExpiry(_ timestamp: Int64?, noExpiry: Bool = false, now: Int64) -> BenefitExpiry {
    if noExpiry { return BenefitExpiry(text: "No expiration", warning: false) }
    guard let timestamp else { return BenefitExpiry(text: "Expiry unknown", warning: false) }
    let seconds = timestamp - now
    if seconds <= 0 { return BenefitExpiry(text: "Expired", warning: false) }
    if seconds < 60 { return BenefitExpiry(text: "Expires in <1m", warning: true) }
    if seconds < 3600 { return BenefitExpiry(text: "Expires in \(Int(ceil(Double(seconds) / 60)))m", warning: true) }
    if seconds < 86400 { return BenefitExpiry(text: "Expires in \(Int(ceil(Double(seconds) / 3600)))h", warning: true) }
    if seconds <= 172800 { return BenefitExpiry(text: "Expires in \(Int(ceil(Double(seconds) / 86400)))d", warning: true) }
    let date = Date(timeIntervalSince1970: TimeInterval(timestamp)).formatted(.dateTime.month(.abbreviated).day().year())
    return BenefitExpiry(text: "Expires \(date)", warning: false)
}

struct BenefitRow: View {
    let benefit: DashboardBenefit
    @Binding var expandedIDs: Set<String>

    private var expanded: Bool { benefit.group.expandable && expandedIDs.contains(benefit.id) }

    var body: some View {
        TimelineView(.periodic(from: .now, by: 60)) { context in
            content(now: Int64(context.date.timeIntervalSince1970))
        }
    }

    private func content(now: Int64) -> some View {
        let group = benefit.group
        let expiry = group.expiry(at: now)
        return VStack(alignment: .leading, spacing: 4) {
            if group.expandable {
                Button {
                    if expanded { expandedIDs.remove(benefit.id) } else { expandedIDs.insert(benefit.id) }
                } label: { summary(group, expiry: expiry, now: now) }
                .buttonStyle(.plain)
                .accessibilityLabel("\(group.title), \(group.valueText(at: now)), \(expiry.text)")
                .accessibilityValue(expanded ? "Expanded" : "Collapsed")
                .accessibilityHint("Shows or hides individual grants and expiration dates")
            } else {
                summary(group, expiry: expiry, now: now)
            }
            if expanded {
                ForEach(group.sortedGrants) { grant in
                    VStack(alignment: .leading, spacing: 2) {
                        HStack(alignment: .firstTextBaseline) {
                            Text(group.amountText(grant.amount) + (grant.expired(at: now) ? " (expired)" : ""))
                            Spacer(minLength: 6)
                            expiration(benefitExpiry(grant.expiresAt, noExpiry: grant.noExpiry == true, now: now))
                        }
                        if let stamp = grant.expiresAt {
                            Text(exactBenefitDate(stamp)).foregroundStyle(.secondary)
                        }
                    }
                    .font(.caption)
                    .padding(.leading, 22)
                    .padding(.vertical, 2)
                    .accessibilityElement(children: .combine)
                }
            }
            if group.missingAmount > 0, expanded || (group.grants ?? []).isEmpty && group.kind != "credits" {
                Text(group.missingText)
                    .font(.caption).foregroundStyle(.secondary).padding(.leading, 22)
            }
            if benefit.provider == "Claude", group.total == nil {
                if group.kind == "api_promo" {
                    Link("API billing not connected · Open billing", destination: URL(string: "https://platform.claude.com/settings/billing")!)
                        .font(.caption).padding(.leading, DashboardColumns.labelInset)
                } else {
                    Link("Check availability in Claude", destination: URL(string: "https://claude.ai/settings/usage")!)
                        .font(.caption).padding(.leading, DashboardColumns.labelInset)
                }
            }
            if benefit.stale || !benefit.failure.isEmpty {
                Text(benefit.failure.isEmpty ? "Stale balance" : "Stale · \(benefit.failure)")
                    .font(.caption).foregroundStyle(.secondary).padding(.leading, 22)
            }
        }
        .padding(.horizontal, DashboardColumns.horizontalPadding)
        .help(helpText(at: now))
    }

    private func summary(_ group: BenefitGroup, expiry: BenefitExpiry, now: Int64) -> some View {
        DashboardRowColumns {
            Text(group.title)
                .padding(.leading, DashboardColumns.labelInset)
        } bar: {
            Color.clear.frame(height: 1)
        } value: {
            Text(group.compactValueText(at: now))
        } status: {
            expiration(group.compactExpiry(at: now))
                .help(expiry.text + "\n* = nearest known expiration; some dates are unavailable")
        }
        .overlay(alignment: .leading) {
            disclosure(group).offset(x: -10)
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(group.title), \(group.valueText(at: now)), \(expiry.text)")
    }

    private func disclosure(_ group: BenefitGroup) -> some View {
        Image(systemName: expanded ? "chevron.down" : "chevron.right")
            .font(.system(size: 9)).frame(width: 6).opacity(group.expandable ? 1 : 0)
            .accessibilityHidden(true)
    }

    private func expiration(_ value: BenefitExpiry) -> some View {
        Text(value.text)
            .foregroundStyle(value.warning ? Color.usageOrange : Color.secondary)
            .fontWeight(value.warning ? .semibold : .regular)
    }

    private func helpText(at now: Int64) -> String {
        var parts = ["\(benefit.group.title): \(benefit.group.valueText(at: now))", benefit.group.expiry(at: now).text,
                     "* = nearest known expiration; some dates are unavailable", benefit.group.note ?? "", "Source: \(benefit.source)"]
        for grant in benefit.group.sortedGrants {
            if let stamp = grant.expiresAt { parts.append("\(benefit.group.amountText(grant.amount)): \(exactBenefitDate(stamp))") }
        }
        if let stamp = benefit.updatedAt { parts.append("Last success: \(exactBenefitDate(stamp))") }
        return parts.filter { !$0.isEmpty }.joined(separator: "\n")
    }
}

private func exactBenefitDate(_ stamp: Int64) -> String {
    let date = Date(timeIntervalSince1970: TimeInterval(stamp))
    let formatter = DateFormatter()
    formatter.dateStyle = .medium
    formatter.timeStyle = .short
    return formatter.string(from: date) + " " + (TimeZone.current.abbreviation(for: date) ?? TimeZone.current.identifier)
}
