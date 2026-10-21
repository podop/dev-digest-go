/** Layout of the agent results: one column per agent, or one tab per agent. */
export type ResultsView = "columns" | "tabs";

export const DEFAULT_VIEW: ResultsView = "columns";

/** Query-string keys of the results page. */
export const VIEW_PARAM = "view";
export const TRACE_PARAM = "trace";
