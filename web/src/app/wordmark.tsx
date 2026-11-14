import "./wordmark.css";

export default function Wordmark() {
  return (
    <span className="wordmark">
      <svg className="wordmark-logo" viewBox="0 0 64 64" width="30" height="30" aria-hidden="true">
        <rect width="64" height="64" fill="currentColor" />
        <g fill="var(--paper)" transform="translate(7.5 9.4) scale(0.72)">
          <rect x="3" y="16" width="17" height="7" />
          <rect x="10" y="16" width="10" height="36" />
          <rect x="3" y="45" width="26" height="7" />
          <path d="M20 27C24 19 31 15 42 16v11C33 25 26 28 20 37z" />
          <rect x="47" y="12" width="15" height="40" />
        </g>
      </svg>
      <span className="wordmark-name">rtfm.run</span>
    </span>
  );
}
