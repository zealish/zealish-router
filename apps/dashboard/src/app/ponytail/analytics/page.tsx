"use client";

import { PageHeader } from "@/components/page-header";
import PonytailAnalyticsPanel from "@/components/ponytail/ponytail-analytics-panel";

export default function PonytailAnalyticsPage() {
  return (
    <>
      <PageHeader
        title="Ponytail Analytics"
        description="Optimization performance metrics."
      />
      <PonytailAnalyticsPanel />
    </>
  );
}