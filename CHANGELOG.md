# CHANGELOG

**v0.7.36 - 24-11-2026**
---

- Reactions sent from Outlook now show as pills in Daak: Outlook's fallback reaction emails (a banner line plus the quoted original, with no machine-readable reaction part) are now detected, linked to the original message and hidden from the list, like Gmail and RFC 9078 reactions. The detector also handles senders that write the reaction headers in different case, and a reaction that arrives while its thread is open now appears without reopening the conversation
- Fixed the launch mismatch where the message list highlighted the first message while the preview pane showed the restored one: the highlighted row now always follows the message actually open in the preview pane. Keyboard navigation (j/k, g/G) and local search now move the preview along with the highlight in classic mode

**v0.7.35 - 23-11-2026**
---

- The reaction email's banner now renders exactly like Outlook's fallback: the whole sentence sits in a pale-blue card, the sender's name is bold, and the banner font is larger than the quoted original below it. Plain-text part, Gmail JSON part and RFC 9078 part unchanged

**v0.7.34 - 23-11-2026**
---

- Reactions are now sent in the Outlook fallback style from the screenshot: the human-readable plain and HTML parts carry a banner ("👍 {name} reacted to your message:") followed by the full original message with its From/Sent/To/Subject headers and complete body. The Gmail JSON part and the RFC 9078 reaction part are unchanged
- Reacting no longer greys out the picker while the reaction sends: the reaction is recorded immediately (the pill shows at once), queued in the Outbox, and sent in the background after the normal Undo delay — the Undo toast appears, and Undo cancels the queued send and removes the pill. After the reaction is sent, removing the pill is local-only (a sent reaction email cannot be retracted). A reaction whose send fails stays in the Outbox with Retry. The detached viewer queues reactions the same way and hands the Undo toast to the main window
- The Outbox no longer keeps history: sent rows are deleted once a send succeeds and cancelled rows are deleted on Undo, in both the normal send queue and the Mail Merge queue. Failed rows stay with Retry. The Sent folder remains the record of sent mail, and the Mail Merge run log remains the record of Mail Merge runs. Mail Merge's hourly budget now reads the run log instead of the queue (direct sends newly count toward the budget too)

**v0.7.33 - 23-11-2026**
---

- Added Empty Spam to the Spam folder's right-click menu in the sidebar: right-clicking an account's Spam folder now offers Empty Spam, with the same confirmation dialog, toasts and list reload as the Empty Spam button in the message list. The entry shows only on Spam folders (account folders only — the unified Spam folder has no context menu)

**v0.7.32 - 23-11-2026**
---

- Fixed Windows toast notifications showing the raw app ID: toasts now show "Daak" with the Daak logo. The sender identity (DisplayName + IconUri) is now written to the registry on every launch, overwriting the stale "net.absons.daak" value left by earlier versions; the installer also ships the app icon next to the exe for it

**v0.7.31 - 21-11-2026**
---

- Fixed the Windows tray menu never showing on any click: the tray window and its message pump now run on the same OS thread (the tray runs on a dedicated locked thread), so left double-click opens the window and right-click shows the menu. The icon was always there — clicks were silently dropped because the pump ran on another thread. No behaviour change: the menu items and click behaviours are unchanged
- Note: on Linux the tray menu on right-click is host-dependent and still needs a live check; if it does not show there either, the fix is to drop the tap handler on Linux so the menu shows on click (see docs/TRAY_AUDIT.md)

**v0.7.30 - 21-11-2026**
---

- Save new send recipients to the sending account's contacts: after a message is sent, each new recipient is saved to that account's Google Contacts, Microsoft contacts, or CardDAV address book, in addition to the local collected contact. New Settings toggle "Save new recipients to account contacts" in Settings → Contacts, on by default. Google and Microsoft accounts need to re-authorize for the new contacts write permission before the provider save works; until then the local collected contact still happens and nothing breaks
- CardDAV auto-discovery: adding an IMAP account now probes for a CardDAV address book (known endpoints for iCloud, Fastmail, GMX, mail.com, mailbox.org and Posteo, then the mail domain and the IMAP host) and adds it as a linked source automatically when one answers, so sent recipients land in the account's address book without manual setup. Best-effort: if nothing is found, account setup proceeds unchanged

**v0.7.29 - 21-11-2026**
---

- Fixed the Flatpak build: the v0.7.28 unread-badge code uses golang.org/x/image/font packages that were missing from the committed Flatpak vendor files, so both Flatpak jobs failed and v0.7.28 shipped without either Flatpak. The vendor files are regenerated; the badge from v0.7.28 now ships on every build

**v0.7.28 - 21-11-2026**
---

- Unread badge: the unified-inbox unread count (inbox folders of enabled accounts; Spam and Trash excluded) now shows on the macOS Dock icon, the Windows taskbar button and the tray/menu-bar icon, capped at 99+. The tray icon carries the number drawn onto it and the tooltip shows the exact count. New Settings toggle "Show unread badge on app and tray icons", on by default. The badge refreshes after every sync and every mark read/unread, and only redraws when the count changed. Linux has no standard taskbar badge API, so the tray icon is its surface

**v0.7.27 - 21-11-2026**
---

- Fixed attachments showing twice: a message with 5 files showed each one twice (10 rows). The batch body-fetch stored attachments with delete-first, but the on-demand body fetch appended a second copy — the BodyFetched guard is checked before the IMAP fetch, so a batch applying mid-fetch (or two on-demand fetches racing) stored the files again under fresh ids. The on-demand fetch and the server-search fetch now replace the attachment set instead of appending, and a one-time database cleanup removes rows already doubled (keeping the downloaded copy)
- The MIME parser was checked and is not the cause: both realistic message shapes parse to exactly 5 attachments

**v0.7.26 - 20-11-2026**
---

- Mac: v0.7.25's tray fix was incomplete — "Open Daak" from the tray menu still crashed the app with the same segfault, because the native window show was never the crasher. The real cause is the "window:show" event: Wails' ExecJS over-releases its script string when called on the macOS main thread, so the autorelease pool drain messages a freed string. The show no longer dispatches to the main thread (Wails dispatches the native calls itself on every platform), the native show is unconditional again, and on macOS the event goes out on a worker goroutine. Left-click still shows the menu; Windows and Linux behaviour is unchanged
- Docs: added docs/TRAY_AUDIT.md — the full tray interaction matrix (every click combination on macOS, Windows and Linux) with the root-cause evidence

**v0.7.25 - 20-11-2026**
---

- Mac: left-clicking the menu-bar icon now shows the tray menu, as it should — double-clicking it crashed the app with a segfault in the run loop right after showing the window, so the double-click-to-open behaviour is gone on macOS (right-click already showed the menu). Windows and Linux keep double-click to open the window
- Mac: showing the window from the tray menu or a second launch now runs on the main thread, and a show of an already-visible window skips the native call and just focuses the window instead

**v0.7.24 - 20-11-2026**
---

- Mac: fixed a crash on launch when Run in Background was on — the system tray was started from a background thread, and macOS requires the menu-bar icon to be created on the main thread, so the app aborted during startup before the window appeared. The tray now starts on the main thread, and if it ever fails to start the window is shown instead of starting hidden, so a saved setting can never leave the app unopenable again
- Single-instance: launching Daak again while it was still starting up could silently drop the "show" command, leaving no window; the activation callback is now registered before startup and a show arriving early is remembered and honoured once startup completes
- Release files: the macOS DMGs, Windows installers and Linux tarballs now carry the version in their filenames (e.g. Daak-v0.7.24-MacOS-intel.dmg), matching the Flatpak and Linux shim names
- Build: fixed the macOS build, which failed at "Generating bindings" — the tray's main-thread helper used Objective-C in a cgo preamble, which Go compiles as plain C; it now uses pure C (pthread_main_np)
- Note: v0.7.23 carried the first three items above but its macOS build never completed, so no usable v0.7.23 release shipped; everything from v0.7.23 rides here instead

**v0.7.23 - 20-11-2026**
---

- Mac: fixed a crash on launch when Run in Background was on — the system tray was started from a background thread, and macOS requires the menu-bar icon to be created on the main thread, so the app aborted during startup before the window appeared. The tray now starts on the main thread, and if it ever fails to start the window is shown instead of starting hidden, so a saved setting can never leave the app unopenable again
- Single-instance: launching Daak again while it was still starting up could silently drop the "show" command, leaving no window; the activation callback is now registered before startup and a show arriving early is remembered and honoured once startup completes
- Release files: the macOS DMGs, Windows installers and Linux tarballs now carry the version in their filenames (e.g. Daak-v0.7.23-MacOS-intel.dmg), matching the Flatpak and Linux shim names

**v0.7.22 - 20-11-2026**
---

- Window: Daak now remembers whether it was maximized or restored when it closed, and reopens in that state. The saved state is also re-applied after the window is shown at startup, and quitting while the window is hidden or minimized no longer overwrites it
- Send in background with Undo Send: clicking Send now closes the composer gracefully and queues the message in the local Outbox, where it waits for a short undo window (5 seconds by default, changeable in Settings → Composer, 5/10/20/30 seconds) with a countdown toast and an Undo button — Undo returns the message to Drafts and reopens the composer with it intact. When the countdown ends the message is sent in the background (slim progress at the bottom, then a small "Message sent" note with View message); failed sends stay in the Outbox with a Retry option. Note: every send is delayed by the undo window — that is what makes Undo possible, since a message cannot be recalled once it has actually been sent

**v0.7.21 - 20-11-2026**
---

- System tray: the right-click menu now has Compose new message, Check mail now and Settings alongside Open Daak and Quit Daak (separator before Settings). Compose and Settings show the window first, then open the composer / Settings dialog; Check mail now syncs all accounts in the background without opening the window. No unread count shown; the tray still appears only when Run in Background is on

**v0.7.20 - 20-11-2026**
---

- Sidebar: all accounts now start collapsed on launch — only the Unified section is expanded. Expanding an account with its chevron still works during a session; it just does not carry over to the next launch

**v0.7.19 - 20-11-2026**
---

- System tray: double-clicking the tray icon now opens the Daak window — a single click does nothing, and right-clicking shows the menu (Open Daak, Quit Daak)

**v0.7.18 - 20-11-2026**
---

- Flatpak: the Flatpak packages are back — v0.7.17 shipped without them because the vendored Go module list hadn't been regenerated after the new system-tray dependency was added, so the Flatpak build failed
- Also in this release (from v0.7.17, for Flatpak users who never got it): reinstalling on Windows while Daak is running now asks the running app to quit gracefully instead of failing instantly with a locked-file error, and Daak shows a tray icon whenever Run in Background is on

**v0.7.17 - 20-11-2026**
---

- Windows installer: reinstalling while Daak is running now asks the running app to quit gracefully instead of failing instantly with a locked-file error — the installer first checks for the running app's quit event, signals it and waits up to 15 seconds, and only falls back to the file check for older copies without the event
- System tray icon: Daak now shows a tray icon whenever Run in Background is on, so a window closed to the background can be reopened from the tray — left-click or Open Daak shows the window, Quit Daak quits for real

**v0.7.16 - 19-11-2026**
---

- Sidebar: the divider between the Unified section and your accounts now keeps its last position when you quit and reopen Daak — it used to reset to the default every time

**v0.7.15 - 19-11-2026**
---

- Windows installer: upgrading now removes the previous install in the other scope, so an upgrade can never leave two Daak installs behind. Installing for all users removes a per-user install directly; installing just for you over an all-users install asks once and removes it with an elevated helper, or leaves both in place if you decline

**v0.7.14 - 18-11-2026**
---

- New app icon: the Daak envelope now uses a mint-to-emerald gradient (mint #A7F3D0 at the top fading to emerald #047857 at the bottom), in the title bar, the About page, and the app icon itself

**v0.7.13 - 18-11-2026**
---

- Windows installer: if Daak will not stop after the graceful quit request, the installer now offers to force-quit it and continue. Answering No keeps the manual Retry path with its instructions, unchanged, and the "Run in background" setting is never touched, so there is nothing to forget re-enabling. Same in the uninstaller

**v0.7.12 - 18-11-2026**
---

- Fixed the Windows release build: v0.7.11 never shipped a Windows installer because the build machine could not find the NSIS compiler — the release workflow now locates it and stops loudly if it is missing
- Also included from v0.7.11:
  - Windows installer is now per-user by default: installing just for you needs no administrator rights and puts Daak in %LOCALAPPDATA%\Programs\Daak; installing for all users still asks for admin rights and uses Program Files. Upgrades default to the previous install's mode and folder
  - Windows installer now closes a running Daak by itself before updating or uninstalling: it asks Daak to quit gracefully and waits for it, instead of failing on the locked file. If Daak is still running after that, it tells you how to quit it and offers Retry

**v0.7.11 - 18-11-2026**
---

- Windows installer is now per-user by default: installing just for you needs no administrator rights and puts Daak in %LOCALAPPDATA%\Programs\Daak; installing for all users still asks for admin rights and uses Program Files. Upgrades default to the previous install's mode and folder
- Windows installer now closes a running Daak by itself before updating or uninstalling: it asks Daak to quit gracefully and waits for it, instead of failing on the locked file. If Daak is still running after that, it tells you how to quit it and offers Retry

**v0.7.10 - 18-11-2026**
---

- Fixed attachments silently disappearing on Windows: attachment ids are now UUIDs instead of timestamps, so two attachments saved in the same instant can no longer collide and drop one (this was the missing PDF of the bank email)
- Fixed attachments showing twice after a resync: re-fetching a message now replaces its attachment set instead of adding to it

**v0.7.9 - 17-11-2026**
---

- Mac DMG background fixed: the background is now drawn at the window's exact size, so the "Daak" title, the "Your mail. Your way." tagline, the tiles and the drag arrow are all fully visible instead of cut off at the edges
- Also included from v0.7.6, v0.7.7 and v0.7.8:
  - Title bar: the real Daak envelope logo is now shown next to "Daak" instead of the generic outline icon
  - Message list: the unread dot is now shown between the checkbox and the contact photo, fully visible instead of clipped at the edge
  - Mac DMG background restyled in the StableBar style: dark background, big Daak title with the "Your mail. Your way." tagline, the app icon in a tile on the left, an orange drag arrow, and the Applications folder in a tile on the right
  - Windows now ships the setup installer only — the plain portable exe is gone, so each processor has exactly one file: Daak-windows-setup-x64.exe and Daak-windows-setup-arm64.exe
  - Bulk delete Undo: deleting several messages and pressing Undo now restores all of them, not just one
  - Selection bar: Mark read and Mark unread are now a single toggle button that follows the majority — it reads "Mark unread" when most selected messages are read, and flips to "Mark read" once they are all unread
  - Deleting an opened message now closes its view too — the tab closes, the dialog dismisses, and the separate popup window closes (archiving still leaves the view open)
  - Mac versions now ship as a DMG: open it and drag Daak onto the Applications shortcut — no more zip

**v0.7.8 - 17-11-2026**
---

- Title bar: the real Daak envelope logo is now shown next to "Daak" instead of the generic outline icon
- Message list: the unread dot is now shown between the checkbox and the contact photo, fully visible instead of clipped at the edge
- Also includes the v0.7.7 changes — the restyled Mac DMG background and Windows shipping the setup installer only (v0.7.7's release was withdrawn before publishing)

**v0.7.7 - 17-11-2026**
---

- Mac DMG background restyled in the StableBar style: dark background, big Daak title with the "Your mail. Your way." tagline, the app icon in a tile on the left, an orange drag arrow, and the Applications folder in a tile on the right
- Windows now ships the setup installer only — the plain portable exe is gone, so each processor has exactly one file: Daak-windows-setup-x64.exe and Daak-windows-setup-arm64.exe

**v0.7.6 - 17-11-2026**
---

- Bulk delete Undo: deleting several messages and pressing Undo now restores all of them, not just one
- Selection bar: Mark read and Mark unread are now a single toggle button that follows the majority — it reads "Mark unread" when most selected messages are read, and flips to "Mark read" once they are all unread
- Deleting an opened message now closes its view too — the tab closes, the dialog dismisses, and the separate popup window closes (archiving still leaves the view open)
- Mac versions now ship as a DMG: open it and drag Daak onto the Applications shortcut — no more zip

**v0.7.5 - 17-11-2026**
---

- Calendar: linking a Google account now includes calendar access, so the calendar works straight away with no separate calendar link step — if calendar access was declined, the calendar shows its own Connect action instead
- Sidebar divider: it now snaps at the level where it starts hiding accounts or unified folders, and a section scrolls with a visible scrollbar when something in it is hidden
- Archive, Delete and Undo fixed — archiving falls back to All Mail when there is no Archive folder, and Undo restores a message even seconds after it was moved

**v0.7.4 - 16-11-2026**
---

- Sidebar: sections expand only from the chevron — clicking an account name opens that account's inbox, and clicking the Unified heading opens the unified inbox
- Sidebar: the divider just above the Accounts block drags up and down to resize the Unified and Accounts sections, and never hides accounts while they fit in the window

**v0.7.3 - 16-11-2026**
---

- The app logo is narrower than in v0.7.2 — width down 10%, height down 5% (half the width cut), back to the original height

**v0.7.2 - 16-11-2026**
---

- The app logo is slightly shorter than in v0.7.1 — still a touch taller than the original

**v0.7.1 - 16-11-2026**
---

- Settings dialog: the tab bar no longer overlaps the content below it
- Dock icon: the new envelope logo now shows in the Dock instead of the old one
- Double-click the title bar to maximize or restore the window
- Message list: drag the divider as wide as you like, and drag it to the right edge to close the preview pane
- The app logo is slightly taller
- Unified folders: the UNIFIED section in the sidebar is expandable and its folders (Inbox, Starred, Sent, Draft, Spam, Trash, Outbox) open the combined view across all accounts; drag the folders to reorder them
- Google sign-in: one Google client now backs mail, contacts and calendar — the separate testing client is gone

**v0.7.0 - 15-11-2026**
---

- One-time passcodes: a chip on messages with a code copies it in one click, and the message is filed away in Trash
- Needs attention: a strip above the message list flags one-time codes, travel changes, payment failures and delivery problems, with a sidebar entry to match
- Sidebar rework: unified folders on top, accounts docked at the bottom — click an account name for its inbox, expand it with the chevron for its folders
- Unsubscribe: one-click unsubscribe from mailing lists, Gmail behaviour — no confirmation, just a toast
- Message translation: translate any message into your language, with offline language downloads so it works without a connection, plus an online fallback
- Category tabs: switch the message list between All, Personal and Automated messages
- Relicensed from Apache License 2.0 to AGPLv3

**v0.6.0 - 15-11-2026**
---

- Renamed the app to Daak, with the new app ID net.absons.daak and repository github.com/absonsnet/daak; existing data and settings move to the new location automatically
- Message list bulk actions: select messages (click, Ctrl/Cmd-click, Shift-click, Space, Ctrl/Cmd-A) and mark read/unread, archive, delete, move, mark as spam or star them from the header
- Messages can now open in tabs, a dialogue, or a pop-up window, and the preview pane can be turned off in Settings; the folder is the first tab and an open message can switch between Tab, Dialog and Popup window
- Fixed the Flatpak build: regenerated the vendored Go modules (the excelize dependency was missing, which failed every Flatpak build since v0.5.0); added a regeneration script and a CI check so the vendoring files cannot go stale again

**v0.5.2 - 14-11-2026**
---

- Email reactions: react to any message with an emoji (thumbs up, heart, laughing, party, surprised, crying, folded hands) from a button in the message header; the reaction is sent to the original sender and shown as a pill under the message, compatible with Gmail's reaction format
- Incoming reaction emails from Gmail are recognised and shown as reactions under the original message instead of appearing as separate emails
- Mail Merge dialog no longer repeats the composer's From, To, Cc, Bcc, Reply-To and Subject fields — it opens straight at Recipients and reads those values from the draft
- The composer's To field now accepts `{{...}}` template addresses such as `{{Email}}` for mail merge; normal Send is blocked while a template variable is in the recipients

**v0.5.1 - 14-11-2026**
---

- Window controls now follow the operating system and desktop environment: traffic-light buttons on the left on macOS and Ubuntu, on the right on Windows and GNOME/KDE/XFCE — with the button order taken from the desktop environment's own setting
- Windows gets native-style glyph buttons (minimize, maximize, close) instead of the traffic-light circles
- New Settings option to force the buttons left or right (Auto by default), which takes effect without a restart

**v0.5.0 - 14-11-2026**
---

- Mail Merge: turn one draft into one personalised message per recipient, using the same `{{...}}` template syntax as Thunderbird's Mail Merge add-on, including conditionals
- Recipient sources: CSV, JSON, XLSX, and all unified contacts
- Deliver modes: Save as drafts (default), Send Later via a persisted outbox queue, and Send Now — with per-recipient preview, pre-flight validation, and a run log with resume
- Safety cap on recipients per run (optional, default 100) with held-batch dispatch, plus scheduled dispatch from the outbox
- Per-recipient S/MIME and PGP signing

**v0.4.3 - 14-11-2026**
---

- AI Writing Assistant now works in the detached composer window — it was missing there because that window runs its own app with only ComposerApp bound, so the extension now lives in that window too
- Dialog composer is wider (1024px) and the toolbar is more compact — smaller icons and tighter spacing — so the AI Writing button and the Plain text toggle fit on one line
- v0.4.3 changes by ABSONS Technologies (@absonsnet)

**v0.4.2 - 13-11-2026**
---

- Composer: Enter now starts a new paragraph with a visible gap, while Shift+Enter stays a line break inside the same paragraph
- Composer window now uses the native title bar, so it can be resized and dragged on macOS
- Composer opens wider (1000px) so the toolbar fits
- Release workflow: publish now verifies every uploaded asset by name instead of only checking the count, and the manifest step fails fast if a shim is missing from the release — a re-pushed tag could leave publish reporting success while the release ended up empty, which is why v0.4.0 and v0.4.1 both failed at Flatpak; never delete and re-push a release tag, always use a fresh version tag
- v0.4.2 changes by ABSONS Technologies (@absonsnet)

**v0.4.1 - 13-11-2026**
---

- AI Writing Assistant: Gemini API keys in the new `AQ.` format are now accepted (Google AI Studio stopped issuing `AIza` keys)
- AI Writing Assistant: replying now correctly offers only Generate Reply when the draft is empty — the quoted email is no longer mistaken for your draft
- AI Writing Assistant: every action's prompt is now editable in settings — pick an action, edit its instructions, or reset it to the built-in prompt
- Composer toolbar no longer crowds the AI Writing button and the Plain text toggle on narrow windows
- Release workflow: Flatpak job installs the GNOME 50 runtime and SDK to match the manifest, and the publish step now fails loudly if the release ends up with no assets
- v0.4.1 changes by ABSONS Technologies (@absonsnet)

**v0.4.0 - 13-11-2026**
---

- AI Writing Assistant extension: Improve Writing, Fix Grammar, Make Formal, Shorten, Expand, Make Friendly, and Generate Reply actions in the composer (OpenAI, Groq, Gemini, xAI, or a local Ollama endpoint)
- Generate Reply writes a reply from the quoted conversation when the draft is empty
- Quoted history is sent to the provider for context only and is re-attached unchanged on insert
- Release workflow now creates the GitHub release when a version tag is pushed
- v0.4.0 changes by ABSONS Technologies (@absonsnet) — previous releases by the original author

**v0.3.6 - 11-03-2026**
---

- Added Brazilian Portuguese (pt-BR) translation - @sasnascimento
- Added Japanse(ja) translation - @theluckystrike
- Added Turkish (tr) translation - @omertahaoztop
- Sync rework - [docs/SYNC_REWORK.md](docs/SYNC_REWORK.md)
- Improved debug/logging
- Fixed login by sending IMAP ID to servers that require it - [#457](https://github.com/absonsnet/daak/issues/457)
- "Mark all as read/unread" on a folder can now include mail older than the sync period (asks when there is any) - [#458](https://github.com/absonsnet/daak/issues/458)
- Empty folders now say when there are messages older than the sync period
- Gmail accounts archive to All Mail by default - [#464](https://github.com/absonsnet/daak/issues/464)
- Fixed shutdown/reboot when Daak running in the background (SIGTERM now quits) - [#460](https://github.com/absonsnet/daak/issues/460)
- Fixed attachment save failing when the Downloads folder has been moved or does not exist - [#474](https://github.com/absonsnet/daak/issues/474)
- HTML signatures and pasted HTML keep their inline styles, link colours, image sizes, fonts and web-font links in the composer - [#110](https://github.com/absonsnet/daak/issues/110)
- Fixed the signature editor showing blank after switching from HTML back to visual mode
- Fixed extra blank space under images in the composer and signature editor
- Fixed the reading pane showing a blank "(No subject)" conversation after deleting a message - [#260](https://github.com/absonsnet/daak/issues/260)
- Fixed mouse-dead UI after the first-run dialogs - [#448](https://github.com/absonsnet/daak/issues/448)
- Fixed sync stalling on an empty-subject message from servers that send a malformed ENVELOPE - [#463](https://github.com/absonsnet/daak/issues/463)
- Fixed merged duplicate messages staying unread and leaving copies behind on delete/move (actions now apply to every copy in the folder)
- Calendar: changing an event's start now moves the end to keep the duration - [#466](https://github.com/absonsnet/daak/issues/466)
- Calendar: events with Windows time zone IDs (Outlook invites) now sync and expand correctly - [#467](https://github.com/absonsnet/daak/pull/467)
- Fixed the message list not highlighting the message opened from a notification click
- Add Account: Fixed Account Name and Username refilling after being cleared - [#451](https://github.com/absonsnet/daak/issues/451)
- Confined auto-fill to Add Account
- Add Account: Removed auto-capitalizing account name on auto-fill  


**v0.3.5 - 09-21-2026**
---

- Temp fix for launch crash caused by WebKitGTK 2.54.0 in latest runtime - [#431](https://github.com/absonsnet/daak/issues/431)
- Bumped wails to 2.16.0
- Bumped golang.org/x/image v0.41.0
- Bumped golang.org/x/net v0.56.0
- Bumped golang.org/x/sys v0.46.0
- Bumped golang.org/x/text v0.39.0


**v0.3.4 - 09-18-2026**
---

- Added default CC
- Added default BCC - [#341](https://github.com/absonsnet/daak/issues/341)
- Added default reply-to - [#404](https://github.com/absonsnet/daak/issues/404)
- Added merge identical messages in thread - [#222](https://github.com/absonsnet/daak/issues/222)
- Relabeled spellcheck dictionary English to English (US)
- Added spellcheck dictionary: English (UK) - [#417](https://github.com/absonsnet/daak/issues/417)
- Added spellcheck dictionary: Dutch
- Added dot and glow dot optional accents for unread messages
- Added window size persistence - [#217](https://github.com/absonsnet/daak/issues/217)
- Added mini calendar for easier navigation - [#368](https://github.com/absonsnet/daak/issues/368)
- Added Empty Spam button for spam folder - [#239](https://github.com/absonsnet/daak/issues/239)
- New app icon with full hicolor size set + scalable SVG - [#395](https://github.com/absonsnet/daak/issues/395) (Special thanks to @gianlucamazza) - [#99](https://github.com/absonsnet/daak/issues/99) 
- Fixed post delete ghost messages on message list
- Fixed thread sender and preview in message list - [#169](https://github.com/absonsnet/daak/issues/169)
- Fixed comma display name - [#398](https://github.com/absonsnet/daak/issues/398)
- Fixed composer draft format detection - [#420](https://github.com/absonsnet/daak/issues/420)
- Block save all attachments if flatpak - [#384](https://github.com/absonsnet/daak/issues/384)
- Calendar color render adjustments to alleviate [#406](https://github.com/absonsnet/daak/issues/406)
- Fixed Microsoft calendar attachments handling - [#370](https://github.com/absonsnet/daak/issues/370)
- Fixed Contacts pagination - [#278](https://github.com/absonsnet/daak/issues/278)
- Fixed M365 contacts - sync all folders - [#278](https://github.com/absonsnet/daak/issues/278)
- Fixed M365 calendar - kill the silent-zero paths - [#278](https://github.com/absonsnet/daak/issues/278)
- Fixed draft edit logic - [#392](https://github.com/absonsnet/daak/issues/392)


**v0.3.3 - 08-06-2026**
---

- Message list redesign - [#70](https://github.com/absonsnet/daak/issues/70) + [#340](https://github.com/absonsnet/daak/issues/340)
- Added swipe gestures - right select and left delete - [#68](https://github.com/absonsnet/daak/issues/68)
- Added manual config of IMAP/SMTP auth mech
- Added digest auth for contacts and calendars - [#313](https://github.com/absonsnet/daak/issues/313)
- Added Polish translation - PR [#374](https://github.com/absonsnet/daak/pull/374)
- Fixed IMAP/SMTP auto auth mech - [#365](https://github.com/absonsnet/daak/issues/355)
- Fixed glitch when rapid deleting messages
- Refactored to engine-level serialization
- Improved toast notification logic - [#115](https://github.com/absonsnet/daak/issues/115)
- Added states to fix unwanted message list reset - [#348](https://github.com/absonsnet/daak/issues/348)
- Improved CalDAV discovery - [#363](https://github.com/absonsnet/daak/issues/363)
- Basic CarDAV (Mailfence) support - [#366](https://github.com/absonsnet/daak/issues/366)
- Fixed typo in autostart code - [#33](https://github.com/absonsnet/daak/issues/33)
- Updated nb translations - [PR #371](https://github.com/absonsnet/daak/pull/371)
- Added g, G, alt+g, alt+G, alt+c, alt+m shortcuts - [#351](https://github.com/absonsnet/daak/issues/351)
- Backfilled v to contact list
- Fixed .desktop - [#356](https://github.com/absonsnet/daak/issues/356) - [#367](https://github.com/absonsnet/daak/issues/367)
- Fixed attachment leak on reply - [#381](https://github.com/absonsnet/daak/issues/381)


**v0.3.2 - 07-16-2026**
---

- Added Spellcheck for languages using Latin alphabet - [#277](https://github.com/absonsnet/daak/issues/277) 
- Improved profile pic support - [#183](https://github.com/absonsnet/daak/issues/183) - Requires force resync of contacts
- Added re-auth button to contact write
- Improved unread count and IDLE sync - [#327](https://github.com/absonsnet/daak/issues/327)
- True fix for duplicate e-mail add error - [#318](https://github.com/absonsnet/daak/issues/318)
- Fixed calendar auth on add - [#337](https://github.com/absonsnet/daak/issues/337)
- Added guard against wails bridge saturation dervied from calendar rapid view switch
- Fixed CarDAV sync profile pic bug - May require a force resync of contacts
- Fixed toast cutoff in mobile layout - [#339](https://github.com/absonsnet/daak/issues/339)
- Updated i18n translation


**v0.3.1 - 07-08-2026**
---

- Added custom oauth for imap, cardav, and caldav (designed for and tested with [Stalwart](https://stalw.art))
- Custom oauth refresh token handling fix
- Use TOFU cert store for DAV certs
- Added Google meet link support - needs force resync
- Timezone config fix
- Multi-day week and day view fix
- Multi-day monthly view fix - [#304](https://github.com/absonsnet/daak/issues/304)
- Composer body config - [#216](https://github.com/absonsnet/daak/issues/216)
- Attachment parsing improvements - [#307](https://github.com/absonsnet/daak/issues/307)
- Fixed replying with correct identity [#325](https://github.com/absonsnet/daak/issues/325)
- Added error message for adding account with same e-mail - [#318](https://github.com/absonsnet/daak/issues/318)
- Made number of events per day in month view dynamics - [#323](https://github.com/absonsnet/daak/issues/323)
- Fixed icon rendering with newer DEs and compositors - [#316](https://github.com/absonsnet/daak/issues/316)
- Fixed diff tz time display in calendar
- Improved calendar about field rendering - needs force resync
- Fixed calendar link handling


**v0.3.0 - 06-23-2026**
---

- Prepared CardDav infra for extensibility
- Added Extension infrastructure
- ALPHA: Added Contacts extension - shipped disabled
- ALPHA: Added Calendar extension - shipped disabled - [#28](https://github.com/absonsnet/daak/issues/28)
- Updated extension translations:
    - Czech
    - French
    - German
    - Italian
    - Vietnamese
    - Chinese - CN, HK, TW
- Added shortcuts: V for View Message and D for Delete Message
- Added runtime config of client id/secret - [#138](https://github.com/absonsnet/daak/issues/138)
- Added force re-sync of contacts
- Added Vietnamese translation - PR [#232](https://github.com/absonsnet/daak/pull/232)
- Added separate smtp credentials option - [#264](https://github.com/absonsnet/daak/issues/264)
- Added no outgoing server option - [#132](https://github.com/absonsnet/daak/issues/132) [(#134)](https://github.com/absonsnet/daak/pull/134)
- Added fallback for Mailfence and other non-quote-compliant providers - [#209](https://github.com/absonsnet/daak/issues/209)
- Added smtp auto-pre-fill of smtp from imap input - [#179](https://github.com/absonsnet/daak/issues/179)
- Added delete account button in settings accounts tab
- Fixed CardDav remove provider code path to not leave orphaned contacts in db
- Fixed Sent/Draft folder message listing - [#227](https://github.com/absonsnet/daak/issues/227)
- Fixed unified inbox actions - [#234](https://github.com/absonsnet/daak/issues/234)
- Fixed Microsoft admin pre-approved oauth - [#29](https://github.com/absonsnet/daak/issues/29)
- Fixed read/star polluting undo - [#243](https://github.com/absonsnet/daak/issues/243)
- Fixed duplicate unified inbox freeze - [#241](https://github.com/absonsnet/daak/issues/241)
- Fixed unparsible body fetch handling - [#240](https://github.com/absonsnet/daak/issues/240)
- Added incremental flag sync - [#240](https://github.com/absonsnet/daak/issues/240)
- Fixed drag-n-drop inline image - [#224](https://github.com/absonsnet/daak/issues/224)
- Fixed duplicate inline image rendering
- Fixed post action blank conversation pane - [#271](https://github.com/absonsnet/daak/issues/271)
- Fixed separate smtp creds persistence - [#270](https://github.com/absonsnet/daak/issues/270)
- Fixed plaintext reply/fwd - [#285](https://github.com/absonsnet/daak/issues/285)
- Fixed print feature - [#280](https://github.com/absonsnet/daak/issues/280)
- Fixed Windows links - [#261](https://github.com/absonsnet/daak/issues/261)
- Hardened Windows URL/attachment opening - Reported by @freemans32
- Fixed mail with no body + attachment - [#293](https://github.com/absonsnet/daak/issues/293)
- Bumped flatpak build to Gnome 50 runtime


**v0.2.5 - 05-27-2026**
---

- Sync progress indication redesign and shifting folder tree fix - [#204](https://github.com/absonsnet/daak/issues/204)
- Added German translation - PR [#194](https://github.com/absonsnet/daak/pull/194)
- Added Italian translation - PR [#208](https://github.com/absonsnet/daak/pull/208)
- Dark content auto bg color and overrides - [#195](https://github.com/absonsnet/daak/issues/195)
- Added guard rails to prevent accidental close of dialogs - [#201](https://github.com/absonsnet/daak/issues/201) - [#198](https://github.com/absonsnet/daak/issues/198)
- Fixed message list on folder switch bug - [#200](https://github.com/absonsnet/daak/issues/200)
- Fixed detached composer draft ops - [#213](https://github.com/absonsnet/daak/issues/213) - [#214](https://github.com/absonsnet/daak/issues/214)
- Fixed send receipt feature
- Fixed dark themes composer lists - [#215](https://github.com/absonsnet/daak/issues/215)
- Fixed setting dialog layout - [#203](https://github.com/absonsnet/daak/issues/203)
- Fixed (workaround) folder subscription for non-compliant providers (Microsoft 365, etc) - [#218](https://github.com/absonsnet/daak/issues/218)
- Code cleanup prior to diving into v0.3.0


**v0.2.4 - 05-20-2026**
---

- Improved oAuth browser open - [#120](https://github.com/absonsnet/daak/issues/120)
- Added copy link for oAuth - [#120](https://github.com/absonsnet/daak/issues/120)
- Added dark mail content option - [#49](https://github.com/absonsnet/daak/issues/49)
- Use desktop portal for email links first and fallback to xdg-open if it fails
- Added -version flag - [#167](https://github.com/absonsnet/daak/issues/167)
- Added setup exe and default app registration for Windows - [#149](https://github.com/absonsnet/daak/issues/149)
- Added Norwegian translation - [#150](https://github.com/absonsnet/daak/issues/150)
- Fixed dark to light core theme switch bug - [#187](https://github.com/absonsnet/daak/issues/187)


**v0.2.3 - 05-14-2026**
---

- Added Czech translation
- Added drag-and-drop to move messages to another folder
- Added cross account copy/move mail - [#108](https://github.com/absonsnet/daak/issues/108)
- Added draggable recipients in composer - [#111](https://github.com/absonsnet/daak/issues/111)
- Added auto-commit recipient on lost focus - [#85](https://github.com/absonsnet/daak/issues/85)
- Added composer del/backspace guard to prevent accidental message delete
- Fixed detached composer system theme detection - [#153](https://github.com/absonsnet/daak/issues/153)
- Fixed launch flow - [#154](https://github.com/absonsnet/daak/issues/154)
- Fixed dark theme rendering - [#155](https://github.com/absonsnet/daak/issues/155)
- Added unread count update after background sync to ensure accuracy


**v0.2.2 - 05-09-2026**
---

- Made contact circles themeable
- Added live theme change preview
- Added Adwaita themes (Light/Dark)
- Added Breeze themes (Light/Dark)
- Added Catppuccin themes (Latte/Frappe/Macchiato/Mocha)
- Added Dracula theme
- Added Github themes (Light/Soft Dark/Dark)
- Added Tokyo Night theme
- Added Nord themes (Light/Dark)
- Added Pop! themes (Light/Dark)
- Added VS Code themes (Light/Dark)
- Added Yaru themes (Light/Dark)


**v0.2.1 - 05-07-2026**
---

- Added thread and message focus mode - [#129](https://github.com/absonsnet/daak/issues/129)
- Added contact circle enable/disable
- Added French translation
- Fixed keyboard expand/collapse of focused messages
- Fixed untranslated drop down menu items
- Fixed iCloud Empty Trash - [#136](https://github.com/absonsnet/daak/issues/136)
- Added ESLint for frontend linting


**v0.2.0 - 04-29-2026**
---

- Refactored N+1 query on folder switch - [#117](https://github.com/absonsnet/daak/issues/117)
- Fixed IMAP server side search regression
- Fixed settings tab icons - [#125](https://github.com/absonsnet/daak/issues/125)
- Fixed plain text links - [#113](https://github.com/absonsnet/daak/issues/113)


**v0.1.39 - 04-27-2026**
---

As of 2026-04-26, Daak is CASA Tier 2 certified and verified by Google so oAuth2 sign-ins will no longer be blocked.

v0.1.39 is a major milestone that includes some remaining originally planned basic features and a substantial amount of bug fixes/refinements focused on making existing features and functions much more reliable/stable. It will serve as a solid foundation for us to continue the further development of this mail client.

- Added image block logic to composer to avoid leaks
- Added folder subscription for auto sync - [#83](https://github.com/absonsnet/daak/issues/83)
- Added optional accent bar for unread messages - [#92](https://github.com/absonsnet/daak/issues/92)
- Added copy text in viewer context menu - [#77](https://github.com/absonsnet/daak/issues/77)
- Added select all in viewer context menu - [#77](https://github.com/absonsnet/daak/issues/77)
- Enable native context menu in composer - [#77](https://github.com/absonsnet/daak/issues/77)
- Added Shared Mailbox support for Microsoft 365 - [#93](https://github.com/absonsnet/daak/issues/93)
- Improved copy/move to folder selection dialog
- Improved invalid encoding handling
- Fixed another ghost message issue
- Fixed invalid e-mail date hang
- Fixed identity switch on replies and forwards
- Fixed draft save and send race condition
- Added proper smtp connect timeout
- Added proper IMAP STARTTLS connect timeout
- Fixed inefficient serilization for inline images and attachments
- Added inline image and attachment size limit
- Fixed inline images on replies and forwards
- Fixed reply/forward signature placement
- Added AttachConsole to for Windows builds for debug output
- Fixed murena.io CardDav - [#86](https://github.com/absonsnet/daak/issues/86)
- Fixed some HK translations
- Fixed non-UTF filename attachment open and download
- Fixed save to sent folder behavior - [#98](https://github.com/absonsnet/daak/issues/98)
- Improved composer formating
- Fixed unchecked rand.Read() in Cryptographic Code
- Backfilled missing DB error handling
- Added proper panic recovery
- Fixed pasted inline image sending
- Fixed undo delete regression
- Fixed name and subject preview decoding - [#104](https://github.com/absonsnet/daak/issues/104)
- Fixed provider icons consistency - [#102](https://github.com/absonsnet/daak/issues/102)
- Added better pgp and s/mime error feedback
- Added a wider range of PGP keys and S/MIME certs support - [#107](https://github.com/absonsnet/daak/issues/107)

**v0.1.38 - 03-22-2026**
---

- Fixed message list refresh on IDLE sync
- Fixed orphaned deleted messages in message list
- Fixed orphaned sync error messages
- Increased go test coverage
- Bumped to Node 24 (LTS)
- GA: skip manifest commit if test build


**v0.1.37 - 03-18-2026**
---

- Changed copy to and move to folder selection to dialog box instead
- Improved moved message handling
- Fixed copy and delete logic for Gmail
- Fixed threading copies of message across folders together
- Fixed post bulk delete focus - [#81](https://github.com/absonsnet/daak/issues/81)
- Fixed post send conversation refresh
- Fixed default from identity for replies


**v0.1.36 - 03-17-2026**
---

- Fixed username auto-fill in add account dialog
- Fixed attachment warning logic - [#79](https://github.com/absonsnet/daak/issues/79)
- Added u-inbox reload guards for better post sync behavior
- Added additional display render error detection - [#74](https://github.com/absonsnet/daak/issues/74)
- Fixes for [#78](https://github.com/absonsnet/daak/issues/78) and [#76](https://github.com/absonsnet/daak/issues/76):
    - Eliminated duplicate event emission from IDLE body fetch 
    - Eliminated redundant webkit calls
    - Cache image allowlist on frontend
    - Handle messages still downloading better
    - Fixed Timer Leak in scheduleMarkAsRead
    - Increased max concurrent db connections
    - Added stale guards
    - Reload necessary messages only during sync
    - Filter unified inbox reloads to inbox folders only

**Note:** Bumped GA and Flatpak node version to 22


**v0.1.35 - 03-13-2026**
---

- Fixed spinning wheel of death in Image Tab of Settings - [#73](https://github.com/absonsnet/daak/issues/73)
- Bumping Github Actions version


**v0.1.34 - 03-13-2026**
---

- Added Images tab to Settings Dialog to manage Always Load lists
- Security - Remove OAuth debug
- Security - Fix attachment file perms
- Security - Sanitize attachment filename
- Security - Validate paths in OpenFile and OpenFolder
- Security - Fix IPC socket TOCTOU with umask
- Security - Strip CRLF in writeHeader
- Cleanup - Removed dead UnblockRemoteImages function
- Cleanup - image loading logic
- Added CardDAV returning time.RFC1123Z (purelymail) workaround - [#71](https://github.com/absonsnet/daak/issues/71)
- Added CardDAV returning unquoted Etag (mailbox.org) workaround - [#26](https://github.com/absonsnet/daak/issues/26)
- Fixed message list checkboxes not responding to shift click - [#67](https://github.com/absonsnet/daak/issues/67)

**Note:** This release is compiled with a new Client ID from the Microsoft newly verified account. However, as per [#29](https://github.com/absonsnet/daak/issues/29), it still doesn't completely solve the oauth "Admin Approval" problem unless your Microsoft 365 administrator has intentionally switched to approve Microsoft verified apps (not the default) in the org settings.


**v0.1.33 - 03-11-2026**
---

- Added dynamic title to detached composer
- Fixed frontend warning
- Updated npm packages
- Improved flags values guarding
- Fixed show attachment in folder - [#69](https://github.com/absonsnet/daak/issues/69)
- Fixed downloading synthetically named attachments
- Added proper flatpak attachments opening from toast logic
- Improved has_attachment marking


**v0.1.32 - 03-10-2026**
---

- Fix - Use reply-to on replies - [#64](https://github.com/absonsnet/daak/issues/64)
- Fixed shift + click selection regression - [#67](https://github.com/absonsnet/daak/issues/67)
- Fixed detached composer title bar - [#65](https://github.com/absonsnet/daak/issues/65)
- Fixed start hidden busy cursor - [#66](https://github.com/absonsnet/daak/issues/66)


**v0.1.31 - 03-05-2026**
---

- Fixed title bar setting regression - [#57](https://github.com/absonsnet/daak/issues/57)


**v0.1.30 - 03-05-2026**
---

- Extracted theme logic from App.svelte into a dedicated Svelte store
- Added a Dark (Balanced) theme
- Added a Light (Balanced) theme
- Added tables and HTML mode to signature composer
- Added option to use native title bar/decorations - [#53](https://github.com/absonsnet/daak/issues/53)
- Added display of reply-to, cc, and bcc if not empty - [#54](https://github.com/absonsnet/daak/issues/54)
- Added always load image setting - [#40](https://github.com/absonsnet/daak/issues/40)
- Fixed Cosmic Desktop bug - needs testing - [#55](https://github.com/absonsnet/daak/issues/55)
- Added workaround instructions for GPU driver bugs - [#56](https://github.com/absonsnet/daak/issues/56)


**v0.1.29 - 02-26-2026**
---

- Toast message to provide feedback for successful link clicks
- Cross accounts from field
- Handle external mailto calls
- Added composer tab in settings 
- Allow setting detached composer as default
- Choose default or detached composer to handle mailto links
- Allow setting plaintext as default
- Moved read receipt setting to composer tab
- Cross account from field
- Fixed drag and drop inline images and attachments - [#41](https://github.com/absonsnet/daak/issues/41)
- Fixed star buttons and states - [#42](https://github.com/absonsnet/daak/issues/42)
- Fixed links in threads [#48](https://github.com/absonsnet/daak/issues/48)
- Fixed attachment logic and extraction for non-text parts - needs a force resync to apply
- Fixed orphaned drafts - [#47](https://github.com/absonsnet/daak/issues/47)
- Fixed flatpak attachment download - [#51](https://github.com/absonsnet/daak/issues/51)
- Consolidated duplicate code between composer and detached composer
- Close conversation viewer if deleted
- Don't auto-open next message if in vertical mobile layout - [#30](https://github.com/absonsnet/daak/issues/30)
- Fixed empty from field - [#39](https://github.com/absonsnet/daak/issues/39)


**v0.1.28 - 02-24-2026**
---

- Slight visual adjustments to the message list checkboxes
- Always show checkboxes on message list when in vertical mobile layout - [#30](https://github.com/absonsnet/daak/issues/30)
- Added per folder filters for unread, starred, and attachments - [#37](https://github.com/absonsnet/daak/issues/37)
- Refactored MessageList.svelte for better maintainability and performance
- Resuming Flathub submission

**Note** to **Flathub** users: A massive amount of features and fixes were in v0.1.25 - v0.1.27 which were not released to Flathub. Check the [Release Page](https://github.com/absonsnet/daak/releases) to see these changes.


**v0.1.27 - 02-23-2026**
---

- Made IMAP folders with sub-folders collapsible
- Identity aware PGP and S/MIME
- Improved guard rails for PGP and S/MIME import, sign, encrypt, and decrypt
- Added multi-language support for missing dynamic message translations
- Proper flatpak implementation of autostart on login - [#33](https://github.com/absonsnet/daak/issues/33)
- Fixed nested IMAP folders fetching - [#34](https://github.com/absonsnet/daak/issues/34)
- Fixed empty or encrypted body preview in message list


**v0.1.26 - 02-22-2026**
---

- Fixed delete silently failing on proton and other generic providers - [#31](https://github.com/absonsnet/daak/issues/31)


**v0.1.25 - 02-21-2026**
---

- Added run in background - [#15](https://github.com/absonsnet/daak/issues/15)
- Added launch hidden - [#15](https://github.com/absonsnet/daak/issues/15)
- Added launch on startup - [#15](https://github.com/absonsnet/daak/issues/15)
- Added Wake and net detection for Windows and Mac
- Added Clickable notifications for Windows and Mac
- Added Empty Trash button for Trash folders - [#21](https://github.com/absonsnet/daak/issues/21)
- Added multi-language support foundation - [#10](https://github.com/absonsnet/daak/issues/10)
- Added 中文(香港), 中文(台灣), 中文(中国)
- Added IMAP search - [#24](https://github.com/absonsnet/daak/issues/24)
- Added Responsive layout to handle both tiling and mobile - [#8](https://github.com/absonsnet/daak/issues/8)
- Fixed sync race condition when moving message during post move sync
- Fixed Trash folder detection to include Bin
- Cleaned up and reorganized sync engine code to be more maintainable

**Note:** Not submitting this release to Flathub until [this issue](https://github.com/flathub/net.absons.daak/issues/6) is resolved.


**v0.1.24 - 02-18-2026**
---

- GMail app password fix - [#22](https://github.com/absonsnet/daak/issues/22)
- Fixed dialog boxes blurry fonts - [#23](https://github.com/absonsnet/daak/issues/23)
- Added context menu to folder pane - [#21](https://github.com/absonsnet/daak/issues/21)
- Close conversation viewer when a message is marked as unread
- Added right alt for triggering context menu with keyboard 


**v0.1.23 - 02-16-2026**
---

- Fixed race condition on marking message read when notification clicked


**v0.1.22 - 02-16-2026**
---

- Fixed wake from sleep flow - [#17](https://github.com/absonsnet/daak/issues/17)
- Added proper network state monitoring
- Improved wake, scheduled syncs, idle, and status logic with net state
- Added proper logic for offline mode
- Fixed S/MIME algo - [#13](https://github.com/absonsnet/daak/issues/13)


**v0.1.21 - 02-14-2026**
---

- Added PGP support - needs more testing
- Added S/MIME support - needs more testing
- Fixed composer rapid enter lag issue with 0 margin `<p>` instead of `<br>`
- Added auto refresh of draft folder on discard
- Added logic to prevent uneccessary reloads of loaded conversations if there's no change
- Fixed draft synced to server indication regression
- Fixed inserted images and attachments saved in draft folder
- Max window size fix [#4](https://github.com/absonsnet/daak/issues/4)
- Auto-focus to the To: field on launch of new composer and on forwards
- Fixed reliability issues with attach file and insert image
- Fixed deletion while syncing
- Improved dead connections handling which makes wake from sleep more reliable & should fix [#9](https://github.com/absonsnet/daak/issues/9)
- Fixed delete mail from trash [#9](https://github.com/absonsnet/daak/issues/9)
- Added reply, reply-all, and forward of a specific message
- Fixed move mail from trash back to inbox
- Improved Sent Folder detection (Wrong sent folder mapping will break threading)
- Ctrl+A when focused on message list will select all messages [#14](https://github.com/absonsnet/daak/issues/14)
- Ctrl+A when focused on conversation viewer will select all text of the expanded email in viewport
    

**v0.1.20 - 02-11-2026**
---

- Added resolution change detection - [#4](https://github.com/absonsnet/daak/issues/4)
- Added trusted self-signed cert flow and store - [#6](https://github.com/absonsnet/daak/issues/6)
- Improved imap login logic
- Improved image blocking to include CSS loaded images
- Enabled horizontal scroll in conversation viewer
    

**v0.1.19 - 02-09-2026**
---

- Fixed terms acceptance visibility
- Enhanced system theme detection
- Fixed idle.go/server.go
- Implemented a workaround for calling dialog through portal
- Removed redundant desktop-file-edit commands from Flatpak manifest
    

**v0.1.18 - 02-08-2026**
---

- Converted to Flathub build from source


**v0.1.17 - 02-07-2026**
---

- Added refresh conversation viewer if new mail arrives in the thread
- Added auto scroll to the bottom (newest mail) in conversation viewer on long threads
- GA/Flathub submission fix


**v0.1.16 - 02-07-2026**
---

- Removed flatpak perm that's already allowed by default
- Fixed hash calculation for Flatpak build and Flathub submission


**v0.1.15 - 02-05-2026**
---

- Refactored Linux notifications to use org.freedesktop.portal.Desktop
- Kept DBUS direct notifications if launched with --dbus-notify
- Added trigger to refocus to Daak if notification is clicked
- Added `install.sh` and `uninstall.sh` to Linux binary release
- Distribute binary tarballs with assets instead of just binary for Linux
- Fixed flatpak app ID
- Flathub submission fixes
- New Github Actions worksflow that makes much more sense


**v0.1.14 - 02-05-2026**
---

- Finalized flatpak submission


**v0.1.13 - 02-04-2026**
---

- Fixed links that don't open in browser (ie. Linkedin, etc)
- Added show link on hover
- Added context menu for links so users can choose to copy the link instead of clicking it directly


**v0.1.12 - 02-03-2026**
---

- Removed AppImage build
- Implemented Flatpak build


**v0.1.11 - 02-02-2026**
---

- Fixed detached composer theme
- Fixed message focus on refresh
- Improved transitions for smoother UX


**v0.1.10 - 02-02-2026**
---

- Added other themes:
    - Dark (Gray)
    - Light (Blue)
    - Light (Orange)


**v0.1.9 - 01-29-2026**
---

- Ability to disable window title bar in settings
- Added an AppImage just for Immutable/Atomic distros [#1](https://github.com/absonsnet/daak/issues/1)


**v0.1.8 - 01-29-2026**
---

- Fixed AppImage support for more popular immutable/atomic distros


**v0.1.7 - 01-29-2026**
---

- Fixed AppImage regression for non-atomic distros
- Sticking with 22.04 LTS to build since 20.04 doesn't have webkit2gtk-4.1 and 20.04 is only a few months away from EOS.


**v0.1.6 - 01-28-2026**
---

- Fixed signature insertion on reply
- Fixed replies not being tracked in conversations
- Fixed ghost recipient on reply-All 
- Cleaned up console.log/warn in frontend
- Added ability to delete single message from conversation
- Sync draft folder after saving draft from inline composer
- Reload conversation viewer after saving draft
- Added keyboard driven single message delete (focus on conversation viewer pane --> tab to msg --> delete)


**v0.1.5 - 01-27-2026**
---

- Bundle icons instead of downloading on launch
- Improved AppImage compatibility


**v0.1.4 - 01-26-2026**
---

- Fixed delete flow regression
- Fixed null reference errors


**v0.1.3 - 01-25-2026**
---

- Added "Mark as NOT Spam" to spam folders
- Improved Google contact sync error handling
- Auto-focus on the first message of search results on enter
- Added cancel folder sync
- Added shortcut keys for sync all accounts and folder sync


**v0.1.2 - 01-22-2026**
---

- Looses keyboard control if e-mail content was clicked
- Autofocus on first message when switched to new folder
- Disable focus on conversation viewer when links are clicked


**v0.1.1 - 01-19-2026**
---

- Compile AppImage with Ubuntu 22.04 instead to improve compatibility with older systems


**v0.1.0 - 01-16-2026**
---

- First release - ALPHA
