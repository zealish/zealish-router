"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { clearToken, getToken, ROUTER_URL, setToken } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

const NAV = [
  { href: "/", label: "Overview" },
  { href: "/models", label: "Models" },
  { href: "/providers", label: "Providers" },
  { href: "/keys", label: "API Keys" },
  { href: "/settings", label: "Settings" },
];

export function Shell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const [token, setLocalToken] = useState<string | null>(null);

  useEffect(() => {
    const sync = () => setLocalToken(getToken());
    sync();
    window.addEventListener("zealish-token", sync);
    return () => window.removeEventListener("zealish-token", sync);
  }, []);

  if (token === null) return null;
  if (token === "") return <TokenGate />;

  return (
    <div className="flex min-h-full flex-col">
      <header className="flex items-center gap-6 border-b px-6 py-3">
        <span className="font-heading text-sm font-semibold">
          Zealish Router
        </span>
        <nav className="flex flex-1 gap-1">
          {NAV.map((item) => (
            <Link
              key={item.href}
              href={item.href}
              className={cn(
                "rounded-md px-3 py-1.5 text-sm",
                pathname === item.href
                  ? "bg-muted text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {item.label}
            </Link>
          ))}
        </nav>
        <span className="text-muted-foreground hidden text-xs sm:inline">
          {ROUTER_URL}
        </span>
        <Button variant="ghost" size="sm" onClick={clearToken}>
          Sign out
        </Button>
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 px-6 py-8">
        {children}
      </main>
    </div>
  );
}

function TokenGate() {
  const [value, setValue] = useState("");

  return (
    <div className="flex min-h-full flex-1 items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Admin token</CardTitle>
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
