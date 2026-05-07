import { createRoot } from "react-dom/client";
import "../globals.css";
import { ChannelsPage } from "@/components/channels/ChannelsPage";

const root = document.getElementById("channels-root");
if (root) {
  createRoot(root).render(<ChannelsPage />);
}
