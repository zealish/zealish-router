"use client";

import Link from "next/link";
import Image from "next/image";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import {
  KeyRound,
  LayoutDashboard,
  Layers,
  LogOut,
  Network,
  Server,
  Settings,
} from "lucide-react";
import { clearToken, getToken, ROUTER_URL, setToken } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { ThemeToggle } from "@/components/theme-toggle";

const NAV = [
  { href: "/", label: "Overview", icon: LayoutDashboard },
  { href: "/providers", label: "Providers", icon: Server },
  { href: "/combos", label: "Combos", icon: Layers },
  { href: "/proxies", label: "Proxy Pool", icon: Network },
  { href: "/keys", label: "Endpoint & Keys", icon: KeyRound },
  { href: "/settings", label: "Settings", icon: Settings },
];

export function Shell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const [token, setLocalToken] = useState<string | null>(null);
  const [scrolled, setScrolled] = useState(false);

  useEffect(() => {
    const sync = () => setLocalToken(getToken());
    sync();
    window.addEventListener("zealish-token", sync);
    return () => window.removeEventListener("zealish-token", sync);
  }, []);

  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 8);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  if (token === null) return null;
  if (token === "") return <TokenGate />;

  const active = NAV.find((item) => item.href === pathname);

  return (
    <div className="flex min-h-full flex-1">
      <aside className="bg-sidebar text-sidebar-foreground sticky top-0 hidden h-dvh w-60 shrink-0 flex-col border-r p-4 md:flex">
        <div className="flex items-center gap-2 px-2 py-3">
          <Image
            src="/logo-mark.webp"
            alt=""
            width={32}
            height={32}
            priority
            className="size-8 dark:invert"
          />
          <span className="font-heading text-sm font-semibold tracking-tight">
            Zealish Router
          </span>
        </div>

        <nav className="mt-4 flex flex-1 flex-col gap-1">
          {NAV.map((item) => {
            const Icon = item.icon;
            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm transition-colors",
                  pathname === item.href
                    ? "bg-sidebar-accent text-sidebar-accent-foreground font-medium"
                    : "text-muted-foreground hover:bg-sidebar-accent/60 hover:text-sidebar-accent-foreground",
                )}
              >
                <Icon className="size-4" />
                {item.label}
              </Link>
            );
          })}
        </nav>

        <div className="text-muted-foreground border-t px-3 pt-3 text-xs">
          <span className="font-mono break-all">{ROUTER_URL}</span>
        </div>
      </aside>

      <div className="bg-grid-paper flex min-w-0 flex-1 flex-col">
        <header
          className={cn(
            "sticky top-0 z-20 transition-all duration-300",
            scrolled ? "px-4 pt-3 pb-2" : "px-0 pt-0 pb-0",
          )}
        >
          <div
            className={cn(
              "flex min-h-16 items-center gap-3 py-3 pr-3 pl-4 transition-all duration-300 md:pl-6",
              scrolled
                ? "bg-background/60 supports-[backdrop-filter]:bg-background/45 rounded-xl border shadow-sm backdrop-blur-xl"
                : "bg-background rounded-none border-b",
            )}
          >
            <span className="flex items-center gap-2 md:hidden">
              <Image
                src="/logo-mark.webp"
                alt=""
                width={24}
                height={24}
                className="size-6 dark:invert"
              />
              <span className="font-heading text-sm font-semibold">Zealish</span>
            </span>
            <span className="hidden text-sm font-medium md:inline">
              {active?.label ?? "Dashboard"}
            </span>

            <nav className="flex flex-1 gap-1 overflow-x-auto md:hidden">
              {NAV.map((item) => (
                <Link
                  key={item.href}
                  href={item.href}
                  className={cn(
                    "rounded-full px-3 py-1 text-xs whitespace-nowrap",
                    pathname === item.href
                      ? "bg-muted text-foreground"
                      : "text-muted-foreground",
                  )}
                >
                  {item.label}
                </Link>
              ))}
            </nav>
            <div className="hidden flex-1 md:block" />

            <ThemeToggle />
            <Button
              variant="ghost"
              size="sm"
              className="rounded-full"
              onClick={clearToken}
            >
              <LogOut className="size-4" />
              <span className="hidden sm:inline">Sign out</span>
            </Button>
          </div>
        </header>

        <main className="mx-auto w-full max-w-6xl flex-1 px-6 py-6">
          {children}
        </main>
      </div>
    </div>
  );
}

function TokenGate() {
  const [value, setValue] = useState("");

  return (
    <div className="flex min-h-full flex-1 items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader className="items-center text-center">
          <Image
            src="/logo.webp"
            alt="Zealish Router"
            width={384}
            height={357}
            priority
            className="mx-auto h-24 w-auto dark:invert"
          />
          <CardTitle className="mt-2">Admin token</CardTitle>
        </CardHeader>
        <CardContent>
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              if (value.trim()) setToken(value.trim());
            }}
          >
            <div className="space-y-2">
              <Label htmlFor="token">Token</Label>
              <Input
                id="token"
                type="password"
                autoComplete="off"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder="admin.token from config.yaml"
              />
              <p className="text-muted-foreground text-xs">
                Stored in this browser only. The router is expected at{" "}
                {ROUTER_URL}.
              </p>
            </div>
            <Button type="submit" className="w-full">
              Continue
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
