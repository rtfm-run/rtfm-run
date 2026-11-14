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
  title: "rtfm.run: your docs, tested on every push",
  description:
    "An AI agent follows your documentation on a fresh machine, finds every step that breaks, fixes the doc, proves the fix works and opens a pull request.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" className={`${courier.variable} ${hand.variable}`}>
      <body>{children}</body>
    </html>
  );
}
