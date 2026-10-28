import { Badge } from "@/components/ui/badge";
import type { Finding } from "@/lib/types";

export function EvidenceAssessment({ finding }: { finding: Finding }) {
  const state = finding.evidence_assessment ?? (finding.evidence?.trim() ? "needs_review" : "needs_evidence");
  const labels = {
    needs_evidence: "Lead · evidence pending",
    needs_review: "Evidence awaiting review",
    supported_for_goal: "Goal evidence reviewed",
    unsupported_for_goal: "Evidence does not support goal",
  };
  return (
    <Badge variant="outline" title="Evidence review and finding status are tracked separately; traffic or a confirmed status cannot replace mechanism evidence.">
      {labels[state]}
    </Badge>
  );
}
