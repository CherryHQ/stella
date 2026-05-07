import { createRoot } from "react-dom/client";
import "../globals.css";
import { SchedulerPage } from "@/components/scheduler/SchedulerPage";

const root = document.getElementById("scheduler-root");
if (root) {
  createRoot(root).render(<SchedulerPage />);
}
