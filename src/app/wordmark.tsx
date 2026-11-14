import "./wordmark.css";

export default function Wordmark() {
  return (
    <span className="wordmark">
      <svg className="wordmark-logo" viewBox="0 0 64 64" width="30" height="30" aria-hidden="true">
        <g fill="currentColor">
          <rect x="2" y="16" width="20" height="7" />
          <rect x="24" y="16" width="20" height="7" />
          <path d="M7 20h10l6 19 6-19h10L27 52h-8z" />
          <rect x="47" y="12" width="15" height="40" />
        </g>
      </svg>
      rtfm.run
    </span>
  );
}
