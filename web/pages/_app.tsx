import type { AppProps } from 'next/app';
import { useEffect } from 'react';
import '../styles/globals.css';

export default function App({ Component, pageProps }: AppProps) {
  useEffect(() => {
    // Earlier versions kept a copy of the page in the browser. Nothing is kept there now, so clear what they left.
    navigator.serviceWorker?.getRegistrations?.().then((all) => all.forEach((r) => r.unregister())).catch(() => {});
    globalThis.caches?.keys?.().then((keys) => keys.forEach((k) => caches.delete(k))).catch(() => {});
    try {
      indexedDB.deleteDatabase('otelgenie');
      indexedDB.deleteDatabase('otelgen');
    } catch {}
  }, []);
  return <Component {...pageProps} />;
}
