import { useId } from "react";

type BrandMarkProps = {
  className?: string;
  title?: string;
};

/** Midnight's crescent-over-horizon mark. Kept as code-native SVG so it
 * remains crisp in the compact project rail and at high browser zoom. */
export function BrandMark({ className = "h-5 w-5", title }: BrandMarkProps) {
  const maskId = `midnight-crescent-${useId().replaceAll(":", "")}`;
  return (
    <svg
      viewBox="0 0 256 256"
      className={className}
      fill="none"
      focusable="false"
      aria-hidden={title ? undefined : "true"}
      role={title ? "img" : undefined}
    >
      {title ? <title>{title}</title> : null}
      <mask id={maskId} maskUnits="userSpaceOnUse" x="0" y="0" width="256" height="256">
        <rect width="256" height="256" fill="white" />
        <circle cx="148" cy="98" r="58" fill="black" />
      </mask>
      <circle cx="122" cy="112" r="58" fill="currentColor" mask={`url(#${maskId})`} />
      <path
        d="M64 184h128"
        stroke="currentColor"
        strokeWidth="12"
        strokeLinecap="round"
      />
    </svg>
  );
}
