"use client";

import { useEffect, useState } from "react";

/**
 * useTick re-renders the caller every `ms` and returns the current epoch ms.
 * Used to drive live "elapsed" timers and relative timestamps.
 */
export function useTick(ms = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(id);
  }, [ms]);
  return now;
}
