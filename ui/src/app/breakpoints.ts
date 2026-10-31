/**
 * Windows at least 840 px wide get the app layout: a fixed navigation pane
 * beside a scrolling content pane. Narrower ones (phones) get a top bar whose
 * menu button opens the navigation over the page.
 */
export const wideWindow = "@media (min-width: 840px)";
/** The query for narrow windows, for useMediaQuery. */
export const narrowQuery = "(max-width: 839.98px)";
export const narrowWindow = `@media ${narrowQuery}`;
