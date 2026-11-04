import * as React from "react";

const MOBILE_BREAKPOINT = 768;
// With the sidebar expanded, tables and cards overlap once the content gets narrow
// (768-1279px). Below this width the sidebar collapses to icon mode automatically.
export const TABLET_BREAKPOINT = 1280;

function useBreakpoint(breakpoint: number) {
  const [below, setBelow] = React.useState<boolean | undefined>(undefined);

  React.useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${breakpoint - 1}px)`);
    const onChange = () => setBelow(window.innerWidth < breakpoint);
    mql.addEventListener("change", onChange);
    setBelow(window.innerWidth < breakpoint);
    return () => mql.removeEventListener("change", onChange);
  }, [breakpoint]);

  return below;
}

export function useIsMobile() {
  return !!useBreakpoint(MOBILE_BREAKPOINT);
}

// Whether the window is narrower than the desktop breakpoint. Before the first
// measurement it returns undefined, so callers can tell "unknown" from false.
export function useIsTablet() {
  return useBreakpoint(TABLET_BREAKPOINT);
}
