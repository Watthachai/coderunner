import type { Metadata } from "next";
import type { ReactNode } from "react";
import "./globals.css";

export const metadata: Metadata = {
  title: "CRN Dashboard",
  description: "FITT Code Runner — build daemon operator dashboard",
};

// Root layout. Single dark shell (midnight studio: black canvas, one accent).
export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
