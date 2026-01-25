const API_BASE = (typeof window !== "undefined" && (window.location.port === "5173" || window.location.port === "5174"))
    ? `${window.location.protocol}//${window.location.hostname}:5172`
    : "";
const RPC_URL = `${API_BASE}/api/rpc`;

export interface RpcResponse<T> {
    result: T;
    error: any;
    id: number;
}

const SPECIALIZED_ENDPOINTS: Record<string, string> = {
    'RPCHandler.GetPR': '/api/get-pr',
    'RPCHandler.AddComment': '/api/add-comment',
    'RPCHandler.EditComment': '/api/edit-comment',
    'RPCHandler.DeleteComment': '/api/delete-comment',
    'RPCHandler.SyncPR': '/api/sync-pr',
    'RPCHandler.SubmitReview': '/api/submit-review',
    'RPCHandler.GetAllReviews': '/api/reviews',
    'RPCHandler.ListPlugins': '/api/list-plugins',
    'RPCHandler.GetPluginOutput': '/api/get-plugin-output',
    'RPCHandler.CheckRepoExists': '/api/check-repo-exists',
};

export async function rpcCall<T>(method: string, params: any[]): Promise<T> {
    const id = Date.now();
    const specializedPath = SPECIALIZED_ENDPOINTS[method];

    const url = specializedPath ? `${API_BASE}${specializedPath}` : RPC_URL;
    const body = specializedPath
        ? (method === 'RPCHandler.GetAllReviews' || method === 'RPCHandler.ListPlugins' ? {} : params[0])
        : { method, params, id };

    const response = await fetch(url, {
        method: "POST",
        headers: {
            "Content-Type": "application/json",
        },
        body: JSON.stringify(body),
    });

    const data: any = await response.json();

    // For specialized endpoints, the server returns { result: ... }
    // For generic RPC, it returns { result: { result: ... } } because bridge.call returns res.result
    // Wait, let's check server.ts implementation. 
    // bridge.call returns res.result (the Go result field).
    // server.ts wraps it: return new Response(JSON.stringify({ result }), ...)
    // So both return { result: ... } where ... is the Go reply struct.

    if (data.error) {
        throw new Error(JSON.stringify(data.error));
    }
    return data.result;
}
