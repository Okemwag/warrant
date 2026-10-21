import { api } from "../api";
import { ApprovalCard, ErrorNote, Loading, useToast } from "../components/ui";
import { useLoad } from "../hooks";
import { loadPending } from "../pending";

export function Approvals({ onChanged }: { onChanged: () => void }) {
  const { data, error, loading, reload } = useLoad(loadPending, [], 4000);
  const toast = useToast();

  const answer = async (id: string, action: "approve" | "decline") => {
    try {
      await api.answer(id, action);
      toast(action === "approve" ? "Approved. Your agent can check out." : "Declined. The hold was released.");
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
    await reload();
    onChanged();
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Waiting for you</h1>
          <p>Purchases your agent wants to make that need your OK. Each one holds its budget for 15 minutes.</p>
        </div>
      </div>
      <ErrorNote error={error} />
      {loading && !data && <Loading />}
      {data && data.length === 0 && (
        <div className="card empty">
          <h2>Nothing waiting</h2>
          <p>When your agent proposes something above your approval line or at a new store, it shows up here.</p>
        </div>
      )}
      <div className="stack">
        {data?.map(({ decision, permission }) => (
          <ApprovalCard key={decision.decision_id} decision={decision} permissionName={permission.name} onAnswer={answer} />
        ))}
      </div>
    </>
  );
}
