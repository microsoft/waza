import { createRoot } from "react-dom/client";
import ManifestAggregate from "../../src/lab/ManifestAggregate";
import "../../src/lab/lab.css";

const root = document.getElementById("root");
if (!root) throw new Error("Aggregate caller harness root is missing.");
createRoot(root).render(<ManifestAggregate demo={false} />);
