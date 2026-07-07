import type { Octokit } from "@octokit/rest";
import { RequestError } from "@octokit/request-error";

export type OrgSecretCheckResult =
  | { kind: "ok"; exists: boolean }
  | { kind: "forbidden" }
  | { kind: "error"; message: string };

/**
 * Minimal GitHub surface used by read-only layer Analyze ports.
 * Implemented with Octokit in the SPA; mocked in Vitest.
 */
export type LayerGithub = {
  getRepoExists(owner: string, repo: string): Promise<boolean>;
  /** File body as UTF-8, or null when the path is missing (404). */
  getRepoFileUtf8(owner: string, repo: string, path: string): Promise<string | null>;
  repoSecretExists(owner: string, repo: string, secretName: string): Promise<boolean>;
  repoVariableExists(owner: string, repo: string, varName: string): Promise<boolean>;
  orgSecretExists(org: string, secretName: string): Promise<OrgSecretCheckResult>;
};

function isNotFound(err: unknown): boolean {
  return err instanceof RequestError && err.status === 404;
}

function decodeContentBase64(b64: string): string {
  const normalized = b64.replace(/\n/g, "");
  let binary: string;
  try {
    binary = atob(normalized);
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    throw new Error(`GitHub file content is not valid base64: ${msg}`, { cause: e });
  }
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return new TextDecoder("utf-8").decode(bytes);
}

export function createLayerGithub(octokit: Octokit): LayerGithub {
  return {
    async getRepoExists(owner, repo) {
      try {
        await octokit.repos.get({ owner, repo });
        return true;
      } catch (err) {
        if (isNotFound(err)) return false;
        throw err;
      }
    },

    async getRepoFileUtf8(owner, repo, path) {
      try {
        const { data } = await octokit.repos.getContent({
          owner,
          repo,
          path,
        });
        if (Array.isArray(data)) return null;
        if (data.type !== "file" || !("content" in data) || typeof data.content !== "string") {
          return null;
        }
        return decodeContentBase64(data.content);
      } catch (err) {
        if (isNotFound(err)) return null;
        throw err;
      }
    },

    async repoSecretExists(owner, repo, secretName) {
      try {
        await octokit.request("GET /repos/{owner}/{repo}/actions/secrets/{secret_name}", {
          owner,
          repo,
          secret_name: secretName,
        });
        return true;
      } catch (err) {
        if (isNotFound(err)) return false;
        throw err;
      }
    },

    async repoVariableExists(owner, repo, varName) {
      try {
        await octokit.request("GET /repos/{owner}/{repo}/actions/variables/{name}", {
          owner,
          repo,
          name: varName,
        });
        return true;
      } catch (err) {
        if (isNotFound(err)) return false;
        throw err;
      }
    },

    async orgSecretExists(org, secretName) {
      try {
        const res = await octokit.request("GET /orgs/{org}/actions/secrets/{secret_name}", {
          org,
          secret_name: secretName,
        });
        if (res.status === 200) return { kind: "ok", exists: true };
        return { kind: "ok", exists: false };
      } catch (err) {
        if (isNotFound(err)) return { kind: "ok", exists: false };
        if (err instanceof RequestError && err.status === 403) {
          return { kind: "forbidden" };
        }
        if (err instanceof RequestError) {
          return { kind: "error", message: err.message };
        }
        return { kind: "error", message: String(err) };
      }
    },
  };
}
