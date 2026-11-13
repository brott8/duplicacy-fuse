# GNOME GUI Polish

## Goal and Decisions

Make the GNOME companion's profile list, setup editor, and snapshot-tree picker clearer and easier to navigate without changing the established GTK 4/libadwaita visual style or the mount backend.

- Cover all three screens, with the setup editor as the priority.
- Keep a single scrollable setup editor, grouped into clear sections rather than introducing a wizard.
- Add an explicit `Single` / `Compare` mode control. Show only the fields relevant to the active mode: legacy revision/path for Single; reference revision, comparison revisions, and archived path selection for Compare.
- Keep existing profile-list actions visible as requested. Make Mount/Unmount the prominent action and align Edit, Clear Cache, and Remove as compact secondary actions; retain their current confirmation and mounted-state rules.
- In the snapshot picker, stage path selections locally. `Done` applies them to the editor; `Cancel` or window close discards them.
- Preserve the current profile format and compatibility. An empty `comparison_revisions` list continues to represent a legacy single-revision setup. Do not change FUSE behavior, helper protocol, path matching, or password policy.

## Implementation Tasks

1. In `duplicacy_fuse_gnome/app.py`, reorganize `ProfileEditor` into repository/storage, snapshot view, and mount-option sections. Use the Single/Compare control to show only the active selection fields. Keep profile name and the essential repository/storage/snapshot controls easy to find; move custom mountpoint, cache directory/size, and helper executable into a collapsed Advanced section. Keep Save/Cancel actions visible and preserve inline validation/errors.
2. Simplify the load sequence and feedback: label each step in context, disable dependent controls until storage/snapshot/revision data is ready, show a spinner or in-place loading state during async requests, and keep errors beside the affected section. Switching repository/storage/snapshot must invalidate stale choices without silently losing unrelated profile fields.
3. Refine `MountWindow` profile rows to show a concise summary (repository/storage, single revision or comparison count/path count, mountpoint, and state) with consistent spacing and an easily scanned Mounted/Starting/Failed status. Retain all visible actions, make Mount/Unmount primary, and use accessible labels/tooltips and destructive styling for Remove. Replace the large always-expanded safety paragraph with a short visible warning and expandable details without hiding the read-only, symlink, or plaintext-cache caveats.
4. Redesign `SnapshotTreePicker` as a navigable list with a clear revision title, breadcrumb/up navigation, optional current-folder search, directory navigation separated from selection checkboxes, and file type/size details. Add selected-path count and Clear selection in a stable footer with Cancel and Done. Show distinct loading, empty, and error states; ignore stale async directory responses after navigation or close. Keep password reuse limited to picker memory and clear it when the picker closes.
5. Keep the UI usable at compact and wide window sizes by allowing content sections and rows to shrink/wrap; avoid introducing a new toolkit dependency or custom theme. Use existing Adw/GTK list, group, and status patterns.
6. Update tests only where behavior is testable without a display. Preserve/extend profile compatibility and mode-selection tests; retain the existing helper and password-security tests.

## State and Failure Handling

- The active mode determines which revision/path fields are saved. Existing profiles with no comparison revisions open in Single mode and retain their legacy `revision` and `selected_path` values.
- Switching modes in an unsaved editor hides inactive controls but should not immediately erase their in-memory values. Saving persists the active mode; a failed validation leaves the editor open with the user's entries intact.
- Async storage, snapshot, revision, and directory results must be associated with the selection that initiated them. Ignore late results when the repository, storage, snapshot, current picker path, or picker lifetime has changed.
- Cancel/close in the picker discards only its staged path edits and clears its temporary password. Done commits the staged path list to the setup editor.
- Keep destructive cache clearing and setup removal confirmations. Disable actions in the same mounted states as today.

## Validation

- Run `python3 -m unittest discover -s tests -v` and `python3 -m py_compile duplicacy_fuse_gnome/app.py duplicacy_fuse_gnome/client.py duplicacy_fuse_gnome/profiles.py` in `/home/brott/Code/duplicacy-fuse-gnome`.
- Manually inspect the app in GNOME at compact and wide sizes and in light/dark appearance. Verify empty and populated profile lists; mounted/unmounted/failed states; Single and Compare editor modes; saved legacy profile loading; and picker navigation, search, selection count, Done/Cancel, empty/error/loading states, and encrypted browsing.
- Confirm that all actions remain visible, keyboard focus/order and accessible names remain usable, and the warning still communicates read-only limitations, symlink behavior, and plaintext caching.

## Compatibility and Scope

This is a presentation and interaction cleanup in the adjacent companion repository `/home/brott/Code/duplicacy-fuse-gnome`. Keep existing serialized profile fields and CLI/helper behavior unchanged; do not modify the FUSE backend. Existing single-revision profiles and comparison profiles must continue to mount with their current semantics.
