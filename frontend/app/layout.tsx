import type { Metadata } from "next";
import type { ReactNode } from "react";
import "./globals.css";

export const metadata: Metadata = {
  title: "CRN Dashboard",
  description: "FITT Code Runner — build daemon operator dashboard",
};

// Sets data-theme on <html> BEFORE first paint so there is no flash of the
// wrong theme: prefer the operator's saved choice, else the OS preference.
const themeScript = `(function(){try{
  var q = new URLSearchParams(location.search).get('theme');
  var t = (q === 'light' || q === 'dark') ? q : localStorage.getItem('crn-theme');
  if (t !== 'light' && t !== 'dark') {
    t = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  document.documentElement.dataset.theme = t;
}catch(e){}})();`;

// Root layout. Duolingo design system with a light/dark theme switch; the theme
// is a data-theme attribute on <html> (see ThemeToggle + themeScript).
export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeScript }} />
      </head>
      <body>{children}</body>
    </html>
  );
}
