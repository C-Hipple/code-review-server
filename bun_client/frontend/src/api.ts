
const API_URL = "http://localhost:3000/api/rpc";

export interface RpcResponse<T> {
    result: T;
    error: any;
    id: number;
}

export async function rpcCall<T>(method: string, params: any[]): Promise<T> {
    const id = Date.now();
    const response = await fetch(API_URL, {
        method: "POST",
        headers: {
            "Content-Type": "application/json",
        },
        body: JSON.stringify({
            method,
            params,
            id,
        }),
    });

    const data: RpcResponse<T> = await response.json();
    if (data.error) {
        throw new Error(JSON.stringify(data.error));
    }
    return data.result;
}
