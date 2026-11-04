"use client";

import * as React from "react";

// The active project (engagement) is a per-browser choice. Every API request
// carries it in the X-Artex-Project header, so the server filters lists by it
// and creates new tasks and chats in it. "" = all projects.

const STORAGE_KEY = "artex.active-project";
const EVENT = "artex:project-changed";

export const PROJECT_HEADER = "X-Artex-Project";

export function getActiveProjectId(): string {
  if (typeof window === "undefined") return "";
  try {
    return window.localStorage.getItem(STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

export function setActiveProjectId(id: string) {
  try {
    if (id) window.localStorage.setItem(STORAGE_KEY, id);
    else window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // storage unavailable: the choice lasts only for this page view
  }
  window.dispatchEvent(new CustomEvent(EVENT, { detail: id }));
}

/** Headers to add to raw fetch calls so they are scoped like http() requests. */
export function projectHeaders(): Record<string, string> {
  const id = getActiveProjectId();
  return id ? { [PROJECT_HEADER]: id } : {};
}

function subscribe(onChange: () => void) {
  const onStorage = (e: StorageEvent) => {
    if (e.key === STORAGE_KEY) onChange();
  };
  window.addEventListener(EVENT, onChange);
  window.addEventListener("storage", onStorage);
  return () => {
    window.removeEventListener(EVENT, onChange);
    window.removeEventListener("storage", onStorage);
  };
}

/** The active project id ("" = all projects), re-rendering when it changes (also in other tabs). */
export function useActiveProjectId(): string {
  return React.useSyncExternalStore(subscribe, getActiveProjectId, () => "");
}
