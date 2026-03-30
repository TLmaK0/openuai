export namespace agent {
	
	export class SessionInfo {
	    id: string;
	    title: string;
	    model: string;
	    provider: string;
	    messages: number;
	    created_at: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.model = source["model"];
	        this.provider = source["provider"];
	        this.messages = source["messages"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}

}

export namespace eventbus {
	
	export class Stats {
	    events_received: number;
	    events_handled: number;
	    events_dropped: number;
	    errors: number;
	    by_source: Record<string, number>;
	    by_type: Record<string, number>;
	
	    static createFrom(source: any = {}) {
	        return new Stats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.events_received = source["events_received"];
	        this.events_handled = source["events_handled"];
	        this.events_dropped = source["events_dropped"];
	        this.errors = source["errors"];
	        this.by_source = source["by_source"];
	        this.by_type = source["by_type"];
	    }
	}

}

export namespace llm {
	
	export class CostEntry {
	    // Go type: time
	    timestamp: any;
	    model: string;
	    input_tokens: number;
	    output_tokens: number;
	    cost_usd: number;
	
	    static createFrom(source: any = {}) {
	        return new CostEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timestamp = this.convertValues(source["timestamp"], null);
	        this.model = source["model"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cost_usd = source["cost_usd"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CostSummary {
	    total_input_tokens: number;
	    total_output_tokens: number;
	    total_cost_usd: number;
	    entries: CostEntry[];
	
	    static createFrom(source: any = {}) {
	        return new CostSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total_input_tokens = source["total_input_tokens"];
	        this.total_output_tokens = source["total_output_tokens"];
	        this.total_cost_usd = source["total_cost_usd"];
	        this.entries = this.convertValues(source["entries"], CostEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace marketplace {

	export class CatalogEntry {
	    name: string;
	    description: string;
	    icon: string;
	    category: string;
	    command: string;
	    args: string[];
	    env?: Record<string, string>;
	    auth_type: string;
	    auth_label: string;
	    auth_env_var: string;
	    subscribe?: string[];

	    static createFrom(source: any = {}) {
	        return new CatalogEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.icon = source["icon"];
	        this.category = source["category"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.env = source["env"];
	        this.auth_type = source["auth_type"];
	        this.auth_label = source["auth_label"];
	        this.auth_env_var = source["auth_env_var"];
	        this.subscribe = source["subscribe"];
	    }
	}

}

export namespace main {
	
	export class ChatResponse {
	    content: string;
	    input_tokens: number;
	    output_tokens: number;
	    cost_usd: number;
	    model: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cost_usd = source["cost_usd"];
	        this.model = source["model"];
	        this.error = source["error"];
	    }
	}
	export class MCPServerStatus {
	    name: string;
	    command: string;
	    url?: string;
	    auto_start: boolean;
	    connected: boolean;
	    tools: number;
	    resources: number;
	    has_auth: boolean;
	    needs_auth: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MCPServerStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.command = source["command"];
	        this.url = source["url"];
	        this.auto_start = source["auto_start"];
	        this.connected = source["connected"];
	        this.tools = source["tools"];
	        this.resources = source["resources"];
	        this.has_auth = source["has_auth"];
	        this.needs_auth = source["needs_auth"];
	    }
	}

}

export namespace voice {
	
	export class AudioDevice {
	    id: string;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new AudioDevice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	    }
	}

}

