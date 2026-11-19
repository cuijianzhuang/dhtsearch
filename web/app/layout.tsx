import type { Metadata } from "next";
import Script from "next/script";
import { cookies } from "next/headers";
import type { CSSProperties } from "react";
import "./globals.css";
import { fetchSiteConfig } from "@/lib/site";
import { APPEARANCE_COOKIE, parseAppearance, themePreset } from "@/lib/theme";

const DESCRIPTION =
  "干净、无广告的磁力链接搜索引擎。基于 DHT 网络实时收录，自动过滤成人内容与垃圾信息。";

export async function generateMetadata(): Promise<Metadata> {
  const site = await fetchSiteConfig();
  const title = `${site.name} - 干净的磁力链接搜索`;
  return {
    title: {
      default: title,
      template: `%s - ${site.name}`,
    },
    description: DESCRIPTION,
    openGraph: {
      title,
      description: DESCRIPTION,
      siteName: site.name,
      type: "website",
    },
  };
}

const gaId = process.env.NEXT_PUBLIC_GA_ID;

// Applies the visitor's stored appearance before first paint, and
// heals the appearance cookie from localStorage. The server renders
// data-appearance from the visitor's cookie when one is present
// (see RootLayout) and from the admin-set default otherwise; this
// script covers visitors whose choice exists only in localStorage
// (made before the cookie mirror existed, or the cookie expired):
// it applies the stored value during parsing and writes the cookie,
// so the next server render — full load, soft navigation or
// router.refresh() — renders the visitor's choice directly.
//
// This must be a plain <script> element, NOT <Script
// strategy="beforeInteractive">: in the App Router an inline
// next/script is not emitted as an executable script tag — it is
// serialized into the __next_s queue and only runs once the Next.js
// client runtime has loaded and processed the queue. Measured on
// v0.1.3: the stored value was applied 0.1–0.4s after parsing began
// (a visible flash of the admin-default palette on every load), and
// when the JS chunks failed to load at all (stale cached HTML after
// an upgrade, a proxy mangling chunks) it never ran — the visitor's
// choice looked "reverted" on every refresh and navigation. A raw
// script element is parser-blocking and runs while the document is
// still being parsed, before any body content, with no dependency
// on the client bundle.
const THEME_BOOT_SCRIPT = `try{var a=localStorage.getItem("dhtsearch-appearance");if(a==="light"||a==="dark"||a==="system"){document.documentElement.dataset.appearance=a;if(document.cookie.indexOf("dhtsearch-appearance="+a)===-1){document.cookie="dhtsearch-appearance="+a+";path=/;max-age=31536000;samesite=lax";}}}catch(e){}`;

export default async function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const site = await fetchSiteConfig();
  // The visitor's own choice (cookie) wins over the admin default:
  // rendering it server-side is what makes the choice survive
  // router.refresh() and soft navigations, which re-render this
  // layout and would otherwise re-apply the admin default over the
  // boot script's correction. Reading a cookie opts the route into
  // dynamic rendering — every public page already is, and the one
  // static page (admin login) is tiny.
  const cookieStore = await cookies();
  const visitorAppearance = parseAppearance(
    cookieStore.get(APPEARANCE_COOKIE)?.value
  );
  const preset = themePreset(site.themeColor);
  const themeVars = {
    "--pri-base": preset.pri,
    "--pri-hover-base": preset.hover,
    "--pri-ink-light": preset.inkLight,
    "--pri-ink-dark": preset.inkDark,
  } as CSSProperties;
  return (
    <html
      lang="zh-CN"
      className="h-full"
      data-appearance={visitorAppearance ?? site.themeAppearance}
      data-theme-color={preset.key}
      style={themeVars}
      suppressHydrationWarning
    >
      <body className="flex min-h-full flex-col bg-canvas text-ink antialiased">
        <script
          id="theme-boot"
          dangerouslySetInnerHTML={{ __html: THEME_BOOT_SCRIPT }}
          suppressHydrationWarning
        />
        {children}
        {gaId && (
          <>
            <Script
              src={`https://www.googletagmanager.com/gtag/js?id=${gaId}`}
              strategy="afterInteractive"
            />
            <Script id="gtag-init" strategy="afterInteractive">
              {`window.dataLayer = window.dataLayer || [];
                function gtag(){dataLayer.push(arguments);}
                gtag('js', new Date());
                gtag('config', '${gaId}');`}
            </Script>
          </>
        )}
      </body>
    </html>
  );
}
