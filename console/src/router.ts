import { useEffect, useState } from "react";

// Hash routes so the Go server only needs to serve index.html.
export type Route =
  | { page: "permissions" }
  | { page: "new" }
  | { page: "edit"; id: string }
  | { page: "permission"; id: string }
  | { page: "decision"; id: string }
  | { page: "approvals" }
  | { page: "settings" };

export function parse(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  switch (parts[0]) {
    case "new":
      return { page: "new" };
    case "p":
      if (parts[1] && parts[2] === "edit") return { page: "edit", id: parts[1] };
      if (parts[1]) return { page: "permission", id: parts[1] };
      break;
    case "d":
      if (parts[1]) return { page: "decision", id: parts[1] };
      break;
    case "approvals":
      return { page: "approvals" };
    case "settings":
      return { page: "settings" };
  }
  return { page: "permissions" };
}

export function href(r: Route): string {
  switch (r.page) {
    case "permissions":
      return "#/";
    case "new":
      return "#/new";
    case "edit":
      return `#/p/${r.id}/edit`;
    case "permission":
      return `#/p/${r.id}`;
    case "decision":
      return `#/d/${r.id}`;
    case "approvals":
      return "#/approvals";
    case "settings":
      return "#/settings";
  }
}

export function go(r: Route) {
  window.location.hash = href(r);
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parse(window.location.hash));
  useEffect(() => {
    const on = () => {
      setRoute(parse(window.location.hash));
      window.scrollTo(0, 0);
    };
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return route;
}
