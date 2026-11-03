// The AI-client setup: one tab per client, each with the text to copy.

import { useState } from "preact/hooks";
import { clients, tokenPlaceholder } from "./clients";
import { Copy } from "./ui";

export function ClientSetup({ mcpURL, token }: { mcpURL: string; token?: string }) {
  const [id, setID] = useState(clients[0]!.id);
  const c = clients.find((x) => x.id === id) ?? clients[0]!;
  const tok = token || tokenPlaceholder;
  return (
    <div class="client-setup">
      <div class="row tabs" role="tablist" aria-label="AI client">
        {clients.map((x) => (
          <button
            key={x.id}
            type="button"
            role="tab"
            aria-selected={x.id === c.id}
            class={x.id === c.id ? "primary small" : "small"}
            onClick={() => setID(x.id)}
          >
            {x.name}
          </button>
        ))}
      </div>
      <div role="tabpanel" aria-label={c.name}>
        <p>{c.where}</p>
        <Copy text={c.snippet(mcpURL, tok)} label={c.name + " setup"} />
        {c.then && <p class="meta">{c.then}</p>}
      </div>
    </div>
  );
}
