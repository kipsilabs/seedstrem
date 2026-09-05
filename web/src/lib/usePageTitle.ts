import { useEffect } from "react";

const APP_NAME = "seedstrem";

// Keeps the browser tab labelled with the current page so several open
// seedstrem tabs stay distinguishable.
export function usePageTitle(title?: string): void {
  useEffect(() => {
    document.title = title ? `${title} · ${APP_NAME}` : APP_NAME;
    return () => {
      document.title = APP_NAME;
    };
  }, [title]);
}
