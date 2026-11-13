import { createElement, lazy, type ComponentType } from "react";

export type LazyRoute<P extends object> = ComponentType<P> & { preload: () => Promise<unknown> };

let pendingPreload: Promise<unknown> = Promise.resolve();

// React throttles Suspense reveals by about 300 ms. Rendering a preloaded route directly avoids
// that delay on direct loads while in-app navigation keeps its announced loading state.
export function lazyRoute<P extends object>(load: () => Promise<{ default: ComponentType<P> }>): LazyRoute<P> {
  let loaded: ComponentType<P> | undefined;
  const loadOnce = () => load().then((module) => {
    loaded = module.default;
    return module;
  });
  const Lazy = lazy(loadOnce);
  const Route = (props: P) => createElement(loaded ?? Lazy, props);
  return Object.assign(Route, {
    preload: () => {
      pendingPreload = loadOnce();
      return pendingPreload;
    },
  });
}

export function preloadedRoutes(): Promise<void> {
  return pendingPreload.then(
    () => undefined,
    () => undefined,
  );
}
