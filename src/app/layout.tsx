import type { Metadata } from "next";
import { Caveat, Courier_Prime } from "next/font/google";
import "./globals.css";

const courier = Courier_Prime({
  variable: "--font-courier",
  weight: ["400", "700"],
  style: ["normal", "italic"],
  subsets: ["latin"],
});

const hand = Caveat({
  variable: "--font-hand",
  weight: ["600", "700"],
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "RFC 127 — Nobody Has Run Your Quickstart Since You Wrote It",
  description:
    "rtfm.run: an agent that follows your quickstart the way a new developer would, finds every step that breaks, writes the fix, and proves the fix works.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" className={`${courier.variable} ${hand.variable}`}>
      <body>{children}</body>
    </html>
  );
}
