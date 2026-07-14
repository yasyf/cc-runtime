# cc-runtime iOS app (placeholder)

This directory is a placeholder for the cc-runtime iOS client. That client is the
phone surface for answering questions and receiving push notifications.

It is not built yet. The iOS app lands in P3, the iOS-and-APNs phase. It will be a
Swift app with a notification feed and an answer UI. It talks directly to the
local daemon, paired via `cc-runtime pair` over the LAN or tailnet, and receives
push directly via APNs. No relay sits in the path.

Until then this holds only this README. No Xcode project or CI is wired here yet.
That arrives with the P3 implementation.

See the project plan and `AGENTS.md` at the repo root for the full picture.
