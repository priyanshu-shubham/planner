export function CircleIcon() {
  return (
    <svg width="17" height="17" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <circle cx="8" cy="8" r="6.4" stroke="currentColor" strokeWidth="1.2" />
    </svg>
  );
}

export function CheckCircleIcon() {
  return (
    <svg width="17" height="17" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <circle cx="8" cy="8" r="6.4" fill="currentColor" />
      <path d="M5.2 8.2l1.9 1.9 3.7-3.9" stroke="#fff" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

export function FolderIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M1.75 4.25c0-.55.45-1 1-1h3.1c.32 0 .62.15.81.4l.68.9h6.1c.55 0 1 .45 1 1v6.8c0 .55-.45 1-1 1H2.75c-.55 0-1-.45-1-1V4.25z"
        stroke="currentColor"
        strokeWidth="1.1"
        strokeLinejoin="round"
      />
    </svg>
  );
}

// GitBranchIcon is the familiar git branch glyph — a trunk with a branch
// splitting off and merging back — used to mark a plan whose project is a git
// remote identity (vs. a plain folder path).
export function GitBranchIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <circle cx="4.5" cy="3.6" r="1.55" stroke="currentColor" strokeWidth="1.1" />
      <circle cx="4.5" cy="12.4" r="1.55" stroke="currentColor" strokeWidth="1.1" />
      <circle cx="11.5" cy="3.6" r="1.55" stroke="currentColor" strokeWidth="1.1" />
      <path d="M4.5 5.15v5.7" stroke="currentColor" strokeWidth="1.1" strokeLinecap="round" />
      <path
        d="M11.5 5.15v1.6a3.2 3.2 0 0 1-3.2 3.2H6"
        stroke="currentColor"
        strokeWidth="1.1"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export function LinkIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M6.75 4.25l.72-.72a3 3 0 0 1 4.24 4.24l-1.42 1.42a3 3 0 0 1-4.24 0M9.25 11.75l-.72.72a3 3 0 0 1-4.24-4.24l1.42-1.42a3 3 0 0 1 4.24 0M6.55 9.45l2.9-2.9"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export function ArchiveBoxIcon() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M2 3.25h12v2.5H2zM2.9 5.75h10.2v6.25a1 1 0 0 1-1 1H3.9a1 1 0 0 1-1-1V5.75zM6.25 8.25h3.5"
        stroke="currentColor"
        strokeWidth="1.1"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export function UnarchiveIcon() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M2 3.25h12v2.5H2zM2.9 5.75h10.2v6.25a1 1 0 0 1-1 1H3.9a1 1 0 0 1-1-1V5.75zM8 11.5V8m0 0L6.5 9.5M8 8l1.5 1.5"
        stroke="currentColor"
        strokeWidth="1.1"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

// CopyIcon is the familiar two-overlapping-sheets glyph, used on the composer's
// copy-quote button.
export function CopyIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect x="5.25" y="5.25" width="8" height="8" rx="1.2" stroke="currentColor" strokeWidth="1.1" />
      <path
        d="M10.75 5.25V3.95c0-.66-.54-1.2-1.2-1.2H3.95c-.66 0-1.2.54-1.2 1.2v5.6c0 .66.54 1.2 1.2 1.2h1.3"
        stroke="currentColor"
        strokeWidth="1.1"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

// CheckIcon is a bare checkmark, shown briefly after a successful copy.
export function CheckIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path d="M3.5 8.5l3 3 6-6.5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

export function TrashIcon() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M5.5 1.75h5M2 4h12M12.4 4l-.55 8.6a1.5 1.5 0 0 1-1.5 1.4H5.65a1.5 1.5 0 0 1-1.5-1.4L3.6 4M6.5 7v4M9.5 7v4"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export function BotIcon() {
  return (
    <svg width="11" height="11" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect x="2.75" y="5.25" width="10.5" height="7.5" rx="2" stroke="currentColor" strokeWidth="1.3" />
      <path d="M8 5V2.5M6 2.5h4" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      <circle cx="5.9" cy="8.9" r="0.9" fill="currentColor" />
      <circle cx="10.1" cy="8.9" r="0.9" fill="currentColor" />
    </svg>
  );
}

export function PersonIcon() {
  return (
    <svg width="11" height="11" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <circle cx="8" cy="5.2" r="2.7" stroke="currentColor" strokeWidth="1.3" />
      <path d="M2.8 13.8a5.5 5.5 0 0 1 10.4 0" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
    </svg>
  );
}
