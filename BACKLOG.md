# Backlog

RunPilot has a single deployment. Feature changes need no data or
configuration migration; remove legacy code instead of migrating its state.

- **Overview made of widgets.** Overview content comes from widget
  contributions. Move the built-in host dashboard (CPU, memory, disk, GPU) to
  widgets and add the launcher (shortcuts) as a widget. Lift the repository
  rule that first-party plugins do not register Overview widgets when this
  starts.
- **Notifications.** A shared capability that plugins use to report events
  such as a failed backup or task run (for example e-mail or a webhook).
  Design it once for several plugins; Backup and Tasks only show failures on
  their pages and as toasts until then.
- **Backup follow-ups.** Browse files inside a Restic snapshot before
  restoring, and a Backup status widget once Overview is made of widgets.
