import { describe, expect, it } from "vitest";
import { filterByPhrases, parseSearchQuery, textContainsPhrases } from "./searchQuery";

describe("parseSearchQuery", () => {
  it("returns the raw query and no phrases when there are no quotes", () => {
    expect(parseSearchQuery("eval scenario")).toEqual({
      query: "eval scenario",
      phrases: [],
    });
  });

  it("extracts a single quoted phrase", () => {
    expect(parseSearchQuery('"eval scenario"')).toEqual({
      query: "eval scenario",
      phrases: ["eval scenario"],
    });
  });

  it("extracts a phrase surrounded by unquoted terms", () => {
    expect(parseSearchQuery('harness "eval scenario" config')).toEqual({
      query: "harness eval scenario config",
      phrases: ["eval scenario"],
    });
  });

  it("extracts multiple quoted phrases", () => {
    expect(parseSearchQuery('"eval scenario" "harness config"')).toEqual({
      query: "eval scenario harness config",
      phrases: ["eval scenario", "harness config"],
    });
  });

  it("ignores empty quoted strings", () => {
    expect(parseSearchQuery('foo "" bar')).toEqual({
      query: 'foo "" bar',
      phrases: [],
    });
  });

  it("treats unmatched quotes as literal characters", () => {
    expect(parseSearchQuery('"eval scenario')).toEqual({
      query: '"eval scenario',
      phrases: [],
    });
  });

  it("handles a single unquoted word", () => {
    expect(parseSearchQuery("harness")).toEqual({
      query: "harness",
      phrases: [],
    });
  });

  it("handles an empty string", () => {
    expect(parseSearchQuery("")).toEqual({
      query: "",
      phrases: [],
    });
  });

  it("trims whitespace inside quoted phrases", () => {
    expect(parseSearchQuery('"  eval scenario  "')).toEqual({
      query: "eval scenario",
      phrases: ["eval scenario"],
    });
  });

  it("handles a quoted single word", () => {
    expect(parseSearchQuery('"harness"')).toEqual({
      query: "harness",
      phrases: ["harness"],
    });
  });

  it("separates adjacent quoted phrases without whitespace", () => {
    expect(parseSearchQuery('"foo bar""baz qux"')).toEqual({
      query: "foo bar baz qux",
      phrases: ["foo bar", "baz qux"],
    });
  });
});

describe("textContainsPhrases", () => {
  it("returns true when there are no phrases", () => {
    expect(textContainsPhrases("any text", [])).toBe(true);
  });

  it("returns true when the phrase appears in the text", () => {
    expect(
      textContainsPhrases("The eval scenario runner starts here.", ["eval scenario"]),
    ).toBe(true);
  });

  it("returns false when the phrase does not appear adjacent", () => {
    expect(
      textContainsPhrases("The eval of each scenario is different.", ["eval scenario"]),
    ).toBe(false);
  });

  it("matches case-insensitively", () => {
    expect(textContainsPhrases("The Eval Scenario runner.", ["eval scenario"])).toBe(true);
  });

  it("requires all phrases to match", () => {
    expect(
      textContainsPhrases("eval scenario and harness config details", [
        "eval scenario",
        "harness config",
      ]),
    ).toBe(true);

    expect(
      textContainsPhrases("eval scenario but no harness here", [
        "eval scenario",
        "harness config",
      ]),
    ).toBe(false);
  });

  it("returns true for a single-word phrase that appears in text", () => {
    expect(textContainsPhrases("the harness is ready", ["harness"])).toBe(true);
  });

  it("returns false when text is empty and phrases are not", () => {
    expect(textContainsPhrases("", ["eval scenario"])).toBe(false);
  });
});

describe("filterByPhrases", () => {
  const results = [
    { title: "Getting Started", titles: ["Guides"], text: "The eval scenario runner starts here." },
    { title: "Config Reference", titles: ["Guides"], text: "Harness config and eval options." },
    { title: "Eval Overview", titles: ["Concepts"], text: "Each scenario runs independently." },
  ];

  it("returns all results when there are no phrases", () => {
    expect(filterByPhrases(results, [])).toEqual(results);
  });

  it("keeps only results whose text contains the exact phrase", () => {
    const filtered = filterByPhrases(results, ["eval scenario"]);
    expect(filtered).toHaveLength(1);
    expect(filtered[0].title).toBe("Getting Started");
  });

  it("requires all phrases to match", () => {
    const filtered = filterByPhrases(results, ["eval scenario", "harness config"]);
    expect(filtered).toHaveLength(0);
  });

  it("matches phrases against titles as well as text", () => {
    const filtered = filterByPhrases(results, ["eval overview"]);
    expect(filtered).toHaveLength(1);
    expect(filtered[0].title).toBe("Eval Overview");
  });

  it("matches phrases spanning title and text content", () => {
    const filtered = filterByPhrases(
      [{ title: "Scenario", titles: ["Eval"], text: "runner starts here" }],
      ["eval scenario"],
    );
    expect(filtered).toHaveLength(1);
  });

  it("keeps results with no text content (graceful degradation)", () => {
    const sparse = [{ title: "", titles: [], text: undefined as unknown as string }];
    expect(filterByPhrases(sparse, ["anything"])).toHaveLength(1);
  });

  it("is case-insensitive", () => {
    const filtered = filterByPhrases(results, ["EVAL SCENARIO"]);
    expect(filtered).toHaveLength(1);
    expect(filtered[0].title).toBe("Getting Started");
  });

  it("works end-to-end with parseSearchQuery", () => {
    const { phrases } = parseSearchQuery('"eval scenario" guide');
    const filtered = filterByPhrases(results, phrases);
    expect(filtered).toHaveLength(1);
    expect(filtered[0].title).toBe("Getting Started");
  });
});
