import { render } from "preact";
import { loadSession } from "./api";
import { App } from "./app";
import "./style.css";

const root = document.getElementById("app");

if (root) {
  loadSession().then(
    (session) => render(<App session={session} />, root),
    (e: Error) => {
      // 401 already sent the browser to sign in.
      root.textContent = e.message;
    },
  );
}
