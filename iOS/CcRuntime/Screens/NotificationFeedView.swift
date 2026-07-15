import SwiftUI

/// NotificationFeedView shows the machine's notification feed newest-first, built
/// live from the subject streams and arriving pushes. Each row acks with a swipe; a
/// toolbar clears the lot. The feed is local: acking or clearing never touches the
/// daemon.
struct NotificationFeedView: View {
    let feed: FeedStore

    @Environment(\.dismiss) private var dismiss

    private var ordered: [NotificationEntry] {
        feed.entries.reversed()
    }

    var body: some View {
        Group {
            if feed.entries.isEmpty {
                ContentUnavailableView(
                    "No Notifications",
                    systemImage: "bell.slash",
                    description: Text("Notifications from this machine's sessions land here.")
                )
            } else {
                List {
                    ForEach(ordered) { entry in
                        NotificationRow(entry: entry)
                            .swipeActions(edge: .trailing) {
                                Button(role: .destructive) {
                                    feed.ack(entry.id)
                                } label: {
                                    Label("Dismiss", systemImage: "checkmark")
                                }
                            }
                    }
                }
            }
        }
        .navigationTitle("Notifications")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Done") { dismiss() }
            }
            ToolbarItem(placement: .primaryAction) {
                Button("Clear") { feed.clear() }
                    .disabled(feed.entries.isEmpty)
            }
        }
    }
}

private struct NotificationRow: View {
    let entry: NotificationEntry

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: entry.isUrgent ? "exclamationmark.circle.fill" : "bell.fill")
                .foregroundStyle(entry.isUrgent ? AnyShapeStyle(.red) : AnyShapeStyle(.tint))
            VStack(alignment: .leading, spacing: 3) {
                Text(entry.message)
                    .font(.subheadline)
                HStack(spacing: 6) {
                    if let subject = entry.subject {
                        Text(scopeLabel(subject))
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    Text(entry.date, style: .time)
                }
                .font(.caption2)
                .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
    }
}
