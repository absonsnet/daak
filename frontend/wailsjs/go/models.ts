export namespace account {
	
	export class Account {
	    id: string;
	    name: string;
	    email: string;
	    sharedMailboxParentId?: string;
	    imapHost: string;
	    imapPort: number;
	    imapSecurity: string;
	    imapAuthMechanism: string;
	    smtpHost: string;
	    smtpPort: number;
	    smtpSecurity: string;
	    smtpAuthMechanism: string;
	    noOutgoingServer: boolean;
	    smtpUsername: string;
	    replyForwardIdentityId: string;
	    authType: string;
	    username: string;
	    enabled: boolean;
	    orderIndex: number;
	    color: string;
	    syncPeriodDays: number;
	    syncInterval: number;
	    syncAllFolders: boolean;
	    syncFoldersEnabled: boolean;
	    readReceiptRequestPolicy: string;
	    sentFolderPath?: string;
	    draftsFolderPath?: string;
	    trashFolderPath?: string;
	    spamFolderPath?: string;
	    archiveFolderPath?: string;
	    allMailFolderPath?: string;
	    starredFolderPath?: string;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    updatedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Account(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.email = source["email"];
	        this.sharedMailboxParentId = source["sharedMailboxParentId"];
	        this.imapHost = source["imapHost"];
	        this.imapPort = source["imapPort"];
	        this.imapSecurity = source["imapSecurity"];
	        this.imapAuthMechanism = source["imapAuthMechanism"];
	        this.smtpHost = source["smtpHost"];
	        this.smtpPort = source["smtpPort"];
	        this.smtpSecurity = source["smtpSecurity"];
	        this.smtpAuthMechanism = source["smtpAuthMechanism"];
	        this.noOutgoingServer = source["noOutgoingServer"];
	        this.smtpUsername = source["smtpUsername"];
	        this.replyForwardIdentityId = source["replyForwardIdentityId"];
	        this.authType = source["authType"];
	        this.username = source["username"];
	        this.enabled = source["enabled"];
	        this.orderIndex = source["orderIndex"];
	        this.color = source["color"];
	        this.syncPeriodDays = source["syncPeriodDays"];
	        this.syncInterval = source["syncInterval"];
	        this.syncAllFolders = source["syncAllFolders"];
	        this.syncFoldersEnabled = source["syncFoldersEnabled"];
	        this.readReceiptRequestPolicy = source["readReceiptRequestPolicy"];
	        this.sentFolderPath = source["sentFolderPath"];
	        this.draftsFolderPath = source["draftsFolderPath"];
	        this.trashFolderPath = source["trashFolderPath"];
	        this.spamFolderPath = source["spamFolderPath"];
	        this.archiveFolderPath = source["archiveFolderPath"];
	        this.allMailFolderPath = source["allMailFolderPath"];
	        this.starredFolderPath = source["starredFolderPath"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
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
	export class AccountConfig {
	    name: string;
	    displayName: string;
	    email: string;
	    sharedMailboxParentId?: string;
	    imapHost: string;
	    imapPort: number;
	    imapSecurity: string;
	    imapAuthMechanism: string;
	    smtpHost: string;
	    smtpPort: number;
	    smtpSecurity: string;
	    smtpAuthMechanism: string;
	    noOutgoingServer: boolean;
	    smtpUsername: string;
	    smtpPassword: string;
	    replyForwardIdentityId: string;
	    authType: string;
	    username: string;
	    password: string;
	    color: string;
	    syncPeriodDays: number;
	    syncInterval: number;
	    syncAllFolders: boolean;
	    syncFoldersEnabled: boolean;
	    readReceiptRequestPolicy: string;
	    sentFolderPath?: string;
	    draftsFolderPath?: string;
	    trashFolderPath?: string;
	    spamFolderPath?: string;
	    archiveFolderPath?: string;
	    allMailFolderPath?: string;
	    starredFolderPath?: string;
	
	    static createFrom(source: any = {}) {
	        return new AccountConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.displayName = source["displayName"];
	        this.email = source["email"];
	        this.sharedMailboxParentId = source["sharedMailboxParentId"];
	        this.imapHost = source["imapHost"];
	        this.imapPort = source["imapPort"];
	        this.imapSecurity = source["imapSecurity"];
	        this.imapAuthMechanism = source["imapAuthMechanism"];
	        this.smtpHost = source["smtpHost"];
	        this.smtpPort = source["smtpPort"];
	        this.smtpSecurity = source["smtpSecurity"];
	        this.smtpAuthMechanism = source["smtpAuthMechanism"];
	        this.noOutgoingServer = source["noOutgoingServer"];
	        this.smtpUsername = source["smtpUsername"];
	        this.smtpPassword = source["smtpPassword"];
	        this.replyForwardIdentityId = source["replyForwardIdentityId"];
	        this.authType = source["authType"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.color = source["color"];
	        this.syncPeriodDays = source["syncPeriodDays"];
	        this.syncInterval = source["syncInterval"];
	        this.syncAllFolders = source["syncAllFolders"];
	        this.syncFoldersEnabled = source["syncFoldersEnabled"];
	        this.readReceiptRequestPolicy = source["readReceiptRequestPolicy"];
	        this.sentFolderPath = source["sentFolderPath"];
	        this.draftsFolderPath = source["draftsFolderPath"];
	        this.trashFolderPath = source["trashFolderPath"];
	        this.spamFolderPath = source["spamFolderPath"];
	        this.archiveFolderPath = source["archiveFolderPath"];
	        this.allMailFolderPath = source["allMailFolderPath"];
	        this.starredFolderPath = source["starredFolderPath"];
	    }
	}
	export class Identity {
	    id: string;
	    accountId: string;
	    email: string;
	    name: string;
	    isDefault: boolean;
	    signatureHtml?: string;
	    signatureText?: string;
	    signatureEnabled: boolean;
	    signatureForNew: boolean;
	    signatureForReply: boolean;
	    signatureForForward: boolean;
	    signaturePlacement: string;
	    signatureSeparator: boolean;
	    orderIndex: number;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    updatedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Identity(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.accountId = source["accountId"];
	        this.email = source["email"];
	        this.name = source["name"];
	        this.isDefault = source["isDefault"];
	        this.signatureHtml = source["signatureHtml"];
	        this.signatureText = source["signatureText"];
	        this.signatureEnabled = source["signatureEnabled"];
	        this.signatureForNew = source["signatureForNew"];
	        this.signatureForReply = source["signatureForReply"];
	        this.signatureForForward = source["signatureForForward"];
	        this.signaturePlacement = source["signaturePlacement"];
	        this.signatureSeparator = source["signatureSeparator"];
	        this.orderIndex = source["orderIndex"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
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
	export class IdentityConfig {
	    email: string;
	    name: string;
	    signatureHtml?: string;
	    signatureText?: string;
	    signatureEnabled: boolean;
	    signatureForNew: boolean;
	    signatureForReply: boolean;
	    signatureForForward: boolean;
	    signaturePlacement: string;
	    signatureSeparator: boolean;
	
	    static createFrom(source: any = {}) {
	        return new IdentityConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.name = source["name"];
	        this.signatureHtml = source["signatureHtml"];
	        this.signatureText = source["signatureText"];
	        this.signatureEnabled = source["signatureEnabled"];
	        this.signatureForNew = source["signatureForNew"];
	        this.signatureForReply = source["signatureForReply"];
	        this.signatureForForward = source["signatureForForward"];
	        this.signaturePlacement = source["signaturePlacement"];
	        this.signatureSeparator = source["signatureSeparator"];
	    }
	}

}

export namespace app {
	
	export class AccountIdentityGroup {
	    account?: account.Account;
	    identities: account.Identity[];
	
	    static createFrom(source: any = {}) {
	        return new AccountIdentityGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.account = this.convertValues(source["account"], account.Account);
	        this.identities = this.convertValues(source["identities"], account.Identity);
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
	export class AppInfo {
	    name: string;
	    version: string;
	    description: string;
	    website: string;
	    license: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.description = source["description"];
	        this.website = source["website"];
	        this.license = source["license"];
	    }
	}
	export class AttentionItem {
	    messageId: string;
	    threadId: string;
	    folderId: string;
	    accountId: string;
	    accountName: string;
	    accountColor: string;
	    subject: string;
	    type: string;
	    typeLabel: string;
	    // Go type: time
	    date: any;
	    extractKind?: string;
	    extractValue?: string;
	
	    static createFrom(source: any = {}) {
	        return new AttentionItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.messageId = source["messageId"];
	        this.threadId = source["threadId"];
	        this.folderId = source["folderId"];
	        this.accountId = source["accountId"];
	        this.accountName = source["accountName"];
	        this.accountColor = source["accountColor"];
	        this.subject = source["subject"];
	        this.type = source["type"];
	        this.typeLabel = source["typeLabel"];
	        this.date = this.convertValues(source["date"], null);
	        this.extractKind = source["extractKind"];
	        this.extractValue = source["extractValue"];
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
	export class AuthContextInfo {
	    kind: string;
	    identifier: string;
	    email: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new AuthContextInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.identifier = source["identifier"];
	        this.email = source["email"];
	        this.label = source["label"];
	    }
	}
	export class BackfillStatus {
	    remaining: number;
	    done: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BackfillStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.remaining = source["remaining"];
	        this.done = source["done"];
	    }
	}
	export class BergamotLegStatus {
	    from: string;
	    to: string;
	    available: boolean;
	    supported: boolean;
	    version?: string;
	    sizeBytes: number;
	    missing?: string[];
	
	    static createFrom(source: any = {}) {
	        return new BergamotLegStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from = source["from"];
	        this.to = source["to"];
	        this.available = source["available"];
	        this.supported = source["supported"];
	        this.version = source["version"];
	        this.sizeBytes = source["sizeBytes"];
	        this.missing = source["missing"];
	    }
	}
	export class BergamotModelsResult {
	    pairs: translate.DownloadedPair[];
	    totalSize: number;
	
	    static createFrom(source: any = {}) {
	        return new BergamotModelsResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pairs = this.convertValues(source["pairs"], translate.DownloadedPair);
	        this.totalSize = source["totalSize"];
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
	export class CachedTranslationResult {
	    found: boolean;
	    engine?: string;
	    subject?: string;
	    body?: string;
	
	    static createFrom(source: any = {}) {
	        return new CachedTranslationResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.found = source["found"];
	        this.engine = source["engine"];
	        this.subject = source["subject"];
	        this.body = source["body"];
	    }
	}
	export class ComposeMode {
	    accountId: string;
	    mode: string;
	    messageId: string;
	    draftId: string;
	
	    static createFrom(source: any = {}) {
	        return new ComposeMode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountId = source["accountId"];
	        this.mode = source["mode"];
	        this.messageId = source["messageId"];
	        this.draftId = source["draftId"];
	    }
	}
	export class ComposerAttachment {
	    filename: string;
	    contentType: string;
	    size: number;
	    data: string;
	
	    static createFrom(source: any = {}) {
	        return new ComposerAttachment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.contentType = source["contentType"];
	        this.size = source["size"];
	        this.data = source["data"];
	    }
	}
	export class ConnectionTestResult {
	    success: boolean;
	    error?: string;
	    certificateRequired: boolean;
	    certificate?: certificate.CertificateInfo;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.error = source["error"];
	        this.certificateRequired = source["certificateRequired"];
	        this.certificate = this.convertValues(source["certificate"], certificate.CertificateInfo);
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
	export class DecryptedAttachment {
	    filename: string;
	    contentType: string;
	    size: number;
	    isInline: boolean;
	    contentId: string;
	
	    static createFrom(source: any = {}) {
	        return new DecryptedAttachment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.contentType = source["contentType"];
	        this.size = source["size"];
	        this.isInline = source["isInline"];
	        this.contentId = source["contentId"];
	    }
	}
	export class DraftResult {
	    draft?: draft.Draft;
	
	    static createFrom(source: any = {}) {
	        return new DraftResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.draft = this.convertValues(source["draft"], draft.Draft);
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
	export class ExtensionInfo {
	    id: string;
	    name: string;
	    version: string;
	    description: string;
	    author: string;
	    minDaakVersion: string;
	    capabilities: string[];
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ExtensionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.version = source["version"];
	        this.description = source["description"];
	        this.author = source["author"];
	        this.minDaakVersion = source["minDaakVersion"];
	        this.capabilities = source["capabilities"];
	        this.enabled = source["enabled"];
	    }
	}
	export class LinkedAccountInfo {
	    accountId: string;
	    email: string;
	    name: string;
	    provider: string;
	    isLinked: boolean;
	    hasContactScope: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LinkedAccountInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountId = source["accountId"];
	        this.email = source["email"];
	        this.name = source["name"];
	        this.provider = source["provider"];
	        this.isLinked = source["isLinked"];
	        this.hasContactScope = source["hasContactScope"];
	    }
	}
	export class MailtoData {
	    to: string[];
	    cc: string[];
	    bcc: string[];
	    subject: string;
	    body: string;
	
	    static createFrom(source: any = {}) {
	        return new MailtoData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.to = source["to"];
	        this.cc = source["cc"];
	        this.bcc = source["bcc"];
	        this.subject = source["subject"];
	        this.body = source["body"];
	    }
	}
	export class MessageDetection {
	    enabled: boolean;
	    language: string;
	    languageName: string;
	    confidence: number;
	    source: string;
	    showBanner: boolean;
	    target: string;
	    targetName: string;
	    isSecurity: boolean;
	    mode: string;
	    offlineAvailable: boolean;
	    offlineEngine: string;
	    offlineNeedsDownload: boolean;
	    offlineDownloadSize: number;
	    offlineEngineMissing: boolean;
	    offlineSupported: boolean;
	    onlineAllowed: boolean;
	    onlineProvider: string;
	    onlineConsented: boolean;
	    cached?: translate.CachedTranslation;
	
	    static createFrom(source: any = {}) {
	        return new MessageDetection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.language = source["language"];
	        this.languageName = source["languageName"];
	        this.confidence = source["confidence"];
	        this.source = source["source"];
	        this.showBanner = source["showBanner"];
	        this.target = source["target"];
	        this.targetName = source["targetName"];
	        this.isSecurity = source["isSecurity"];
	        this.mode = source["mode"];
	        this.offlineAvailable = source["offlineAvailable"];
	        this.offlineEngine = source["offlineEngine"];
	        this.offlineNeedsDownload = source["offlineNeedsDownload"];
	        this.offlineDownloadSize = source["offlineDownloadSize"];
	        this.offlineEngineMissing = source["offlineEngineMissing"];
	        this.offlineSupported = source["offlineSupported"];
	        this.onlineAllowed = source["onlineAllowed"];
	        this.onlineProvider = source["onlineProvider"];
	        this.onlineConsented = source["onlineConsented"];
	        this.cached = this.convertValues(source["cached"], translate.CachedTranslation);
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
	export class MessageReaction {
	    emoji: string;
	    reactorEmail: string;
	    reactorName: string;
	    // Go type: time
	    createdAt: any;
	    isOwn: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MessageReaction(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emoji = source["emoji"];
	        this.reactorEmail = source["reactorEmail"];
	        this.reactorName = source["reactorName"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.isOwn = source["isOwn"];
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
	export class NotificationPermissionInfo {
	    status: string;
	    platform: string;
	    canOpenSystemSettings: boolean;
	
	    static createFrom(source: any = {}) {
	        return new NotificationPermissionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.platform = source["platform"];
	        this.canOpenSystemSettings = source["canOpenSystemSettings"];
	    }
	}
	export class OAuthBuildStatus {
	    google: boolean;
	    microsoft: boolean;
	
	    static createFrom(source: any = {}) {
	        return new OAuthBuildStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.google = source["google"];
	        this.microsoft = source["microsoft"];
	    }
	}
	export class OAuthCredsChoice {
	    id: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new OAuthCredsChoice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	    }
	}
	export class OAuthCredsChoices {
	    configId: string;
	    choices: OAuthCredsChoice[];
	    current: string;
	    hasUserOverride: boolean;
	    clientIdFingerprint: string;
	
	    static createFrom(source: any = {}) {
	        return new OAuthCredsChoices(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.configId = source["configId"];
	        this.choices = this.convertValues(source["choices"], OAuthCredsChoice);
	        this.current = source["current"];
	        this.hasUserOverride = source["hasUserOverride"];
	        this.clientIdFingerprint = source["clientIdFingerprint"];
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
	export class OAuthCredsStatus {
	    configId: string;
	    hasUserOverride: boolean;
	    hasShipped: boolean;
	    clientIdFingerprint: string;
	
	    static createFrom(source: any = {}) {
	        return new OAuthCredsStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.configId = source["configId"];
	        this.hasUserOverride = source["hasUserOverride"];
	        this.hasShipped = source["hasShipped"];
	        this.clientIdFingerprint = source["clientIdFingerprint"];
	    }
	}
	export class OAuthStatus {
	    isOAuth: boolean;
	    provider: string;
	    email: string;
	    // Go type: time
	    expiresAt: any;
	    isExpired: boolean;
	    needsReauth: boolean;
	
	    static createFrom(source: any = {}) {
	        return new OAuthStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.isOAuth = source["isOAuth"];
	        this.provider = source["provider"];
	        this.email = source["email"];
	        this.expiresAt = this.convertValues(source["expiresAt"], null);
	        this.isExpired = source["isExpired"];
	        this.needsReauth = source["needsReauth"];
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
	export class OIDCDiscoveryResult {
	    authorizationEndpoint: string;
	    tokenEndpoint: string;
	    userinfoEndpoint: string;
	
	    static createFrom(source: any = {}) {
	        return new OIDCDiscoveryResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.authorizationEndpoint = source["authorizationEndpoint"];
	        this.tokenEndpoint = source["tokenEndpoint"];
	        this.userinfoEndpoint = source["userinfoEndpoint"];
	    }
	}
	export class PGPViewResult {
	    bodyHtml: string;
	    bodyText: string;
	    pgpStatus: string;
	    pgpSignerEmail: string;
	    pgpSignerKeyId: string;
	    pgpEncrypted: boolean;
	    inlineAttachments?: Record<string, string>;
	    attachments?: DecryptedAttachment[];
	
	    static createFrom(source: any = {}) {
	        return new PGPViewResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bodyHtml = source["bodyHtml"];
	        this.bodyText = source["bodyText"];
	        this.pgpStatus = source["pgpStatus"];
	        this.pgpSignerEmail = source["pgpSignerEmail"];
	        this.pgpSignerKeyId = source["pgpSignerKeyId"];
	        this.pgpEncrypted = source["pgpEncrypted"];
	        this.inlineAttachments = source["inlineAttachments"];
	        this.attachments = this.convertValues(source["attachments"], DecryptedAttachment);
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
	export class QueueSendResult {
	    queueId: string;
	    draftId: string;
	    delaySeconds: number;
	    sendAt: string;
	
	    static createFrom(source: any = {}) {
	        return new QueueSendResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.queueId = source["queueId"];
	        this.draftId = source["draftId"];
	        this.delaySeconds = source["delaySeconds"];
	        this.sendAt = source["sendAt"];
	    }
	}
	export class SMIMEViewResult {
	    bodyHtml: string;
	    bodyText: string;
	    smimeStatus: string;
	    smimeSignerEmail: string;
	    smimeSignerSubject: string;
	    smimeEncrypted: boolean;
	    inlineAttachments?: Record<string, string>;
	    attachments?: DecryptedAttachment[];
	
	    static createFrom(source: any = {}) {
	        return new SMIMEViewResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bodyHtml = source["bodyHtml"];
	        this.bodyText = source["bodyText"];
	        this.smimeStatus = source["smimeStatus"];
	        this.smimeSignerEmail = source["smimeSignerEmail"];
	        this.smimeSignerSubject = source["smimeSignerSubject"];
	        this.smimeEncrypted = source["smimeEncrypted"];
	        this.inlineAttachments = source["inlineAttachments"];
	        this.attachments = this.convertValues(source["attachments"], DecryptedAttachment);
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
	export class SenderCategoryOverride {
	    senderEmail: string;
	    category: string;
	
	    static createFrom(source: any = {}) {
	        return new SenderCategoryOverride(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.senderEmail = source["senderEmail"];
	        this.category = source["category"];
	    }
	}
	export class SubscriptionSender {
	    senderEmail: string;
	    senderName: string;
	    accountId: string;
	    countAll: number;
	    count30Days: number;
	    lastReceived: string;
	    oneClick: boolean;
	    unsubscribed: boolean;
	    unsubscribeMethod: string;
	    domainMismatch: boolean;
	    autoDelete: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SubscriptionSender(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.senderEmail = source["senderEmail"];
	        this.senderName = source["senderName"];
	        this.accountId = source["accountId"];
	        this.countAll = source["countAll"];
	        this.count30Days = source["count30Days"];
	        this.lastReceived = source["lastReceived"];
	        this.oneClick = source["oneClick"];
	        this.unsubscribed = source["unsubscribed"];
	        this.unsubscribeMethod = source["unsubscribeMethod"];
	        this.domainMismatch = source["domainMismatch"];
	        this.autoDelete = source["autoDelete"];
	    }
	}
	export class TranslationSettings {
	    enabled: boolean;
	    mode: string;
	    targetLanguage: string;
	    resolvedTarget: string;
	    onlineProvider: string;
	    consents: Record<string, boolean>;
	    deeplKeySet: boolean;
	    osEngine: translate.OSEngineStatus;
	
	    static createFrom(source: any = {}) {
	        return new TranslationSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.mode = source["mode"];
	        this.targetLanguage = source["targetLanguage"];
	        this.resolvedTarget = source["resolvedTarget"];
	        this.onlineProvider = source["onlineProvider"];
	        this.consents = source["consents"];
	        this.deeplKeySet = source["deeplKeySet"];
	        this.osEngine = this.convertValues(source["osEngine"], translate.OSEngineStatus);
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
	export class TranslationTargetLanguage {
	    code: string;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new TranslationTargetLanguage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.name = source["name"];
	    }
	}
	export class UndoSendResult {
	    queueId: string;
	    accountId: string;
	    draftId: string;
	
	    static createFrom(source: any = {}) {
	        return new UndoSendResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.queueId = source["queueId"];
	        this.accountId = source["accountId"];
	        this.draftId = source["draftId"];
	    }
	}
	export class UnsubscribeResult {
	    method: string;
	    url: string;
	    domain: string;
	    domainMismatch: boolean;
	    isSpam: boolean;
	    senderEmail: string;
	    senderName: string;
	
	    static createFrom(source: any = {}) {
	        return new UnsubscribeResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.method = source["method"];
	        this.url = source["url"];
	        this.domain = source["domain"];
	        this.domainMismatch = source["domainMismatch"];
	        this.isSpam = source["isSpam"];
	        this.senderEmail = source["senderEmail"];
	        this.senderName = source["senderName"];
	    }
	}
	export class UnsubscribeStatus {
	    messageId: string;
	    available: boolean;
	    alreadyUnsubscribed: boolean;
	    method: string;
	    domain: string;
	    domainMismatch: boolean;
	    isSpam: boolean;
	    senderEmail: string;
	    senderName: string;
	
	    static createFrom(source: any = {}) {
	        return new UnsubscribeStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.messageId = source["messageId"];
	        this.available = source["available"];
	        this.alreadyUnsubscribed = source["alreadyUnsubscribed"];
	        this.method = source["method"];
	        this.domain = source["domain"];
	        this.domainMismatch = source["domainMismatch"];
	        this.isSpam = source["isSpam"];
	        this.senderEmail = source["senderEmail"];
	        this.senderName = source["senderName"];
	    }
	}
	export class ViewMode {
	    accountId: string;
	    folderId: string;
	    folderType: string;
	    threadId: string;
	
	    static createFrom(source: any = {}) {
	        return new ViewMode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountId = source["accountId"];
	        this.folderId = source["folderId"];
	        this.folderType = source["folderType"];
	        this.threadId = source["threadId"];
	    }
	}

}

export namespace appstate {
	
	export class UIState {
	    selectedAccountId: string;
	    selectedFolderId: string;
	    selectedFolderName: string;
	    selectedFolderType: string;
	    selectedThreadId: string;
	    selectedConversationAccountId: string;
	    selectedConversationFolderId: string;
	    sidebarWidth: number;
	    listWidth: number;
	    accountsHeight: number;
	    expandedAccounts: Record<string, boolean>;
	    unifiedInboxExpanded: boolean;
	    collapsedFolders: Record<string, boolean>;
	    activeExtension?: string;
	
	    static createFrom(source: any = {}) {
	        return new UIState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.selectedAccountId = source["selectedAccountId"];
	        this.selectedFolderId = source["selectedFolderId"];
	        this.selectedFolderName = source["selectedFolderName"];
	        this.selectedFolderType = source["selectedFolderType"];
	        this.selectedThreadId = source["selectedThreadId"];
	        this.selectedConversationAccountId = source["selectedConversationAccountId"];
	        this.selectedConversationFolderId = source["selectedConversationFolderId"];
	        this.sidebarWidth = source["sidebarWidth"];
	        this.listWidth = source["listWidth"];
	        this.accountsHeight = source["accountsHeight"];
	        this.expandedAccounts = source["expandedAccounts"];
	        this.unifiedInboxExpanded = source["unifiedInboxExpanded"];
	        this.collapsedFolders = source["collapsedFolders"];
	        this.activeExtension = source["activeExtension"];
	    }
	}

}

export namespace backend {
	
	export class ActionInfo {
	    id: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new ActionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	    }
	}
	export class ActionPromptInfo {
	    id: string;
	    label: string;
	    defaultPrompt: string;
	    customPrompt: string;
	
	    static createFrom(source: any = {}) {
	        return new ActionPromptInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.defaultPrompt = source["defaultPrompt"];
	        this.customPrompt = source["customPrompt"];
	    }
	}
	export class Attendee {
	    email: string;
	    cn?: string;
	    partStat?: string;
	    role?: string;
	    rsvp?: boolean;
	    cuType?: string;
	    delegate?: string;
	    scheduleStatus?: string;
	
	    static createFrom(source: any = {}) {
	        return new Attendee(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.cn = source["cn"];
	        this.partStat = source["partStat"];
	        this.role = source["role"];
	        this.rsvp = source["rsvp"];
	        this.cuType = source["cuType"];
	        this.delegate = source["delegate"];
	        this.scheduleStatus = source["scheduleStatus"];
	    }
	}
	export class AttendeeInput {
	    email: string;
	    cn?: string;
	    partStat?: string;
	    role?: string;
	    rsvp?: boolean;
	    cuType?: string;
	    delegate?: string;
	
	    static createFrom(source: any = {}) {
	        return new AttendeeInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.cn = source["cn"];
	        this.partStat = source["partStat"];
	        this.role = source["role"];
	        this.rsvp = source["rsvp"];
	        this.cuType = source["cuType"];
	        this.delegate = source["delegate"];
	    }
	}
	export class Calendar {
	    id: string;
	    sourceId: string;
	    url: string;
	    displayName: string;
	    description?: string;
	    color?: string;
	    visible: boolean;
	    writable: boolean;
	    ctag?: string;
	    lastSyncedAt: number;
	    createdAt: number;
	
	    static createFrom(source: any = {}) {
	        return new Calendar(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.sourceId = source["sourceId"];
	        this.url = source["url"];
	        this.displayName = source["displayName"];
	        this.description = source["description"];
	        this.color = source["color"];
	        this.visible = source["visible"];
	        this.writable = source["writable"];
	        this.ctag = source["ctag"];
	        this.lastSyncedAt = source["lastSyncedAt"];
	        this.createdAt = source["createdAt"];
	    }
	}
	export class CancelQueuedRunResult {
	    cancelled: number;
	    inFlight: number;
	
	    static createFrom(source: any = {}) {
	        return new CancelQueuedRunResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cancelled = source["cancelled"];
	        this.inFlight = source["inFlight"];
	    }
	}
	export class ContactsResult {
	    rows: any[];
	    columns: string[];
	
	    static createFrom(source: any = {}) {
	        return new ContactsResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rows = source["rows"];
	        this.columns = source["columns"];
	    }
	}
	export class DispatchResult {
	    sent: number;
	    failed: number;
	    remaining: number;
	    queued: number;
	
	    static createFrom(source: any = {}) {
	        return new DispatchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sent = source["sent"];
	        this.failed = source["failed"];
	        this.remaining = source["remaining"];
	        this.queued = source["queued"];
	    }
	}
	export class Organizer {
	    email: string;
	    cn?: string;
	
	    static createFrom(source: any = {}) {
	        return new Organizer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.cn = source["cn"];
	    }
	}
	export class Event {
	    id: string;
	    calendarId: string;
	    uid: string;
	    etag: string;
	    href: string;
	    providerEventId?: string;
	    summary: string;
	    description?: string;
	    descriptionHTML?: string;
	    location?: string;
	    dtstartUnix: number;
	    dtendUnix: number;
	    isAllDay: boolean;
	    tzName?: string;
	    rruleText?: string;
	    transparency?: string;
	    visibility?: string;
	    status?: string;
	    sequence?: number;
	    categories?: string[];
	    exceptionDates?: number[];
	    alarmFingerprints?: string[];
	    attendees?: Attendee[];
	    organizer?: Organizer;
	
	    static createFrom(source: any = {}) {
	        return new Event(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.calendarId = source["calendarId"];
	        this.uid = source["uid"];
	        this.etag = source["etag"];
	        this.href = source["href"];
	        this.providerEventId = source["providerEventId"];
	        this.summary = source["summary"];
	        this.description = source["description"];
	        this.descriptionHTML = source["descriptionHTML"];
	        this.location = source["location"];
	        this.dtstartUnix = source["dtstartUnix"];
	        this.dtendUnix = source["dtendUnix"];
	        this.isAllDay = source["isAllDay"];
	        this.tzName = source["tzName"];
	        this.rruleText = source["rruleText"];
	        this.transparency = source["transparency"];
	        this.visibility = source["visibility"];
	        this.status = source["status"];
	        this.sequence = source["sequence"];
	        this.categories = source["categories"];
	        this.exceptionDates = source["exceptionDates"];
	        this.alarmFingerprints = source["alarmFingerprints"];
	        this.attendees = this.convertValues(source["attendees"], Attendee);
	        this.organizer = this.convertValues(source["organizer"], Organizer);
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
	export class OrganizerInput {
	    email: string;
	    cn?: string;
	
	    static createFrom(source: any = {}) {
	        return new OrganizerInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.cn = source["cn"];
	    }
	}
	export class ReminderSpec {
	    offsetMinutes: number;
	
	    static createFrom(source: any = {}) {
	        return new ReminderSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.offsetMinutes = source["offsetMinutes"];
	    }
	}
	export class RecurrenceSpec {
	    freq: string;
	    untilUnix: number;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new RecurrenceSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.freq = source["freq"];
	        this.untilUnix = source["untilUnix"];
	        this.count = source["count"];
	    }
	}
	export class EventInput {
	    calendarId: string;
	    summary: string;
	    description?: string;
	    descriptionHTML?: string;
	    location?: string;
	    dtstartUnix: number;
	    dtendUnix: number;
	    isAllDay?: boolean;
	    tz?: string;
	    transparency?: string;
	    visibility?: string;
	    recurrence?: RecurrenceSpec;
	    reminder?: ReminderSpec;
	    attendees?: AttendeeInput[];
	    organizer?: OrganizerInput;
	    sendUpdates?: string;
	
	    static createFrom(source: any = {}) {
	        return new EventInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.calendarId = source["calendarId"];
	        this.summary = source["summary"];
	        this.description = source["description"];
	        this.descriptionHTML = source["descriptionHTML"];
	        this.location = source["location"];
	        this.dtstartUnix = source["dtstartUnix"];
	        this.dtendUnix = source["dtendUnix"];
	        this.isAllDay = source["isAllDay"];
	        this.tz = source["tz"];
	        this.transparency = source["transparency"];
	        this.visibility = source["visibility"];
	        this.recurrence = this.convertValues(source["recurrence"], RecurrenceSpec);
	        this.reminder = this.convertValues(source["reminder"], ReminderSpec);
	        this.attendees = this.convertValues(source["attendees"], AttendeeInput);
	        this.organizer = this.convertValues(source["organizer"], OrganizerInput);
	        this.sendUpdates = source["sendUpdates"];
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
	export class EventInstance {
	    id: string;
	    calendarId: string;
	    uid: string;
	    etag: string;
	    href: string;
	    providerEventId?: string;
	    summary: string;
	    description?: string;
	    descriptionHTML?: string;
	    location?: string;
	    dtstartUnix: number;
	    dtendUnix: number;
	    isAllDay: boolean;
	    tzName?: string;
	    rruleText?: string;
	    transparency?: string;
	    visibility?: string;
	    status?: string;
	    sequence?: number;
	    categories?: string[];
	    exceptionDates?: number[];
	    alarmFingerprints?: string[];
	    attendees?: Attendee[];
	    organizer?: Organizer;
	    instanceStartUnix: number;
	    instanceEndUnix: number;
	    isRecurrenceOverride?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new EventInstance(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.calendarId = source["calendarId"];
	        this.uid = source["uid"];
	        this.etag = source["etag"];
	        this.href = source["href"];
	        this.providerEventId = source["providerEventId"];
	        this.summary = source["summary"];
	        this.description = source["description"];
	        this.descriptionHTML = source["descriptionHTML"];
	        this.location = source["location"];
	        this.dtstartUnix = source["dtstartUnix"];
	        this.dtendUnix = source["dtendUnix"];
	        this.isAllDay = source["isAllDay"];
	        this.tzName = source["tzName"];
	        this.rruleText = source["rruleText"];
	        this.transparency = source["transparency"];
	        this.visibility = source["visibility"];
	        this.status = source["status"];
	        this.sequence = source["sequence"];
	        this.categories = source["categories"];
	        this.exceptionDates = source["exceptionDates"];
	        this.alarmFingerprints = source["alarmFingerprints"];
	        this.attendees = this.convertValues(source["attendees"], Attendee);
	        this.organizer = this.convertValues(source["organizer"], Organizer);
	        this.instanceStartUnix = source["instanceStartUnix"];
	        this.instanceEndUnix = source["instanceEndUnix"];
	        this.isRecurrenceOverride = source["isRecurrenceOverride"];
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
	export class EventUpdateInput {
	    eventId: string;
	    originalInstanceTimeUnix?: number;
	    calendarId: string;
	    summary: string;
	    description?: string;
	    descriptionHTML?: string;
	    location?: string;
	    dtstartUnix: number;
	    dtendUnix: number;
	    isAllDay?: boolean;
	    tz?: string;
	    transparency?: string;
	    visibility?: string;
	    recurrence?: RecurrenceSpec;
	    reminder?: ReminderSpec;
	    attendees?: AttendeeInput[];
	    organizer?: OrganizerInput;
	    sendUpdates?: string;
	
	    static createFrom(source: any = {}) {
	        return new EventUpdateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.eventId = source["eventId"];
	        this.originalInstanceTimeUnix = source["originalInstanceTimeUnix"];
	        this.calendarId = source["calendarId"];
	        this.summary = source["summary"];
	        this.description = source["description"];
	        this.descriptionHTML = source["descriptionHTML"];
	        this.location = source["location"];
	        this.dtstartUnix = source["dtstartUnix"];
	        this.dtendUnix = source["dtendUnix"];
	        this.isAllDay = source["isAllDay"];
	        this.tz = source["tz"];
	        this.transparency = source["transparency"];
	        this.visibility = source["visibility"];
	        this.recurrence = this.convertValues(source["recurrence"], RecurrenceSpec);
	        this.reminder = this.convertValues(source["reminder"], ReminderSpec);
	        this.attendees = this.convertValues(source["attendees"], AttendeeInput);
	        this.organizer = this.convertValues(source["organizer"], OrganizerInput);
	        this.sendUpdates = source["sendUpdates"];
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
	export class ExhaustedWrite {
	    id: string;
	    sourceId: string;
	    calendarId: string;
	    eventId: string;
	    op: string;
	    scope: string;
	    summary: string;
	    lastError: string;
	    attempt: number;
	    lastAttemptUnix: number;
	    createdUnix: number;
	
	    static createFrom(source: any = {}) {
	        return new ExhaustedWrite(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.sourceId = source["sourceId"];
	        this.calendarId = source["calendarId"];
	        this.eventId = source["eventId"];
	        this.op = source["op"];
	        this.scope = source["scope"];
	        this.summary = source["summary"];
	        this.lastError = source["lastError"];
	        this.attempt = source["attempt"];
	        this.lastAttemptUnix = source["lastAttemptUnix"];
	        this.createdUnix = source["createdUnix"];
	    }
	}
	export class FreeBusyBlock {
	    email: string;
	    startUnix: number;
	    endUnix: number;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new FreeBusyBlock(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.startUnix = source["startUnix"];
	        this.endUnix = source["endUnix"];
	        this.status = source["status"];
	    }
	}
	export class FreeBusyResult {
	    email: string;
	    blocks: FreeBusyBlock[];
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new FreeBusyResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.blocks = this.convertValues(source["blocks"], FreeBusyBlock);
	        this.source = source["source"];
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
	export class GoogleCalendarChoice {
	    id: string;
	    summary: string;
	    primary: boolean;
	    accessRole: string;
	    writable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GoogleCalendarChoice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.summary = source["summary"];
	        this.primary = source["primary"];
	        this.accessRole = source["accessRole"];
	        this.writable = source["writable"];
	    }
	}
	export class GoogleCalendarSelection {
	    id: string;
	    displayName: string;
	    color?: string;
	    writable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GoogleCalendarSelection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.color = source["color"];
	        this.writable = source["writable"];
	    }
	}
	export class ImproveRequest {
	    actionId: string;
	    draftHtml: string;
	    quotedHtml: string;
	    isNewEmail: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ImproveRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.actionId = source["actionId"];
	        this.draftHtml = source["draftHtml"];
	        this.quotedHtml = source["quotedHtml"];
	        this.isNewEmail = source["isNewEmail"];
	    }
	}
	export class ImproveResult {
	    improvedHtml: string;
	    subjects: string[];
	
	    static createFrom(source: any = {}) {
	        return new ImproveResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.improvedHtml = source["improvedHtml"];
	        this.subjects = source["subjects"];
	    }
	}
	export class MicrosoftCalendarChoice {
	    id: string;
	    name: string;
	    isDefaultCalendar: boolean;
	    writable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MicrosoftCalendarChoice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.isDefaultCalendar = source["isDefaultCalendar"];
	        this.writable = source["writable"];
	    }
	}
	export class MicrosoftCalendarSelection {
	    id: string;
	    displayName: string;
	    color?: string;
	    writable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MicrosoftCalendarSelection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.color = source["color"];
	        this.writable = source["writable"];
	    }
	}
	export class ModelInfo {
	    id: string;
	    label: string;
	    provider: string;
	
	    static createFrom(source: any = {}) {
	        return new ModelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.provider = source["provider"];
	    }
	}
	
	
	export class QueueConfig {
	    perHour: number;
	    paused: boolean;
	    countNormalSends: boolean;
	
	    static createFrom(source: any = {}) {
	        return new QueueConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.perHour = source["perHour"];
	        this.paused = source["paused"];
	        this.countNormalSends = source["countNormalSends"];
	    }
	}
	export class QueueRunStats {
	    runId: string;
	    queued: number;
	    sent: number;
	    failed: number;
	    cancelled: number;
	    paused: boolean;
	
	    static createFrom(source: any = {}) {
	        return new QueueRunStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.runId = source["runId"];
	        this.queued = source["queued"];
	        this.sent = source["sent"];
	        this.failed = source["failed"];
	        this.cancelled = source["cancelled"];
	        this.paused = source["paused"];
	    }
	}
	export class QueueStats {
	    total: number;
	    queued: number;
	    sent: number;
	    failed: number;
	    cancelled: number;
	    runs: QueueRunStats[];
	
	    static createFrom(source: any = {}) {
	        return new QueueStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.queued = source["queued"];
	        this.sent = source["sent"];
	        this.failed = source["failed"];
	        this.cancelled = source["cancelled"];
	        this.runs = this.convertValues(source["runs"], QueueRunStats);
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
	export class QueuedItem {
	    id: string;
	    runId: string;
	    rowIndex: number;
	    accountId: string;
	    recipient: string;
	    subject: string;
	    fromAddress: string;
	    to: string[];
	    status: string;
	    attempts: number;
	    maxAttempts: number;
	    // Go type: time
	    nextAttemptAt: any;
	    lastError?: string;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    sentAt?: any;
	
	    static createFrom(source: any = {}) {
	        return new QueuedItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.runId = source["runId"];
	        this.rowIndex = source["rowIndex"];
	        this.accountId = source["accountId"];
	        this.recipient = source["recipient"];
	        this.subject = source["subject"];
	        this.fromAddress = source["fromAddress"];
	        this.to = source["to"];
	        this.status = source["status"];
	        this.attempts = source["attempts"];
	        this.maxAttempts = source["maxAttempts"];
	        this.nextAttemptAt = this.convertValues(source["nextAttemptAt"], null);
	        this.lastError = source["lastError"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.sentAt = this.convertValues(source["sentAt"], null);
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
	
	
	export class ResizedContactPhoto {
	    data: string;
	    mediaType: string;
	
	    static createFrom(source: any = {}) {
	        return new ResizedContactPhoto(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.data = source["data"];
	        this.mediaType = source["mediaType"];
	    }
	}
	export class Settings {
	    selectedModel: string;
	    temperature: number;
	    maxTokens: number;
	    customApiEndpoint: string;
	    customModel: string;
	    promptImprove: string;
	    actionPrompts: Record<string, string>;
	    useConversationHistory: boolean;
	    hasApiKey: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.selectedModel = source["selectedModel"];
	        this.temperature = source["temperature"];
	        this.maxTokens = source["maxTokens"];
	        this.customApiEndpoint = source["customApiEndpoint"];
	        this.customModel = source["customModel"];
	        this.promptImprove = source["promptImprove"];
	        this.actionPrompts = source["actionPrompts"];
	        this.useConversationHistory = source["useConversationHistory"];
	        this.hasApiKey = source["hasApiKey"];
	    }
	}
	export class SettingsInput {
	    selectedModel: string;
	    temperature: number;
	    maxTokens: number;
	    customApiEndpoint: string;
	    customModel: string;
	    promptImprove: string;
	    actionPrompts: Record<string, string>;
	    useConversationHistory: boolean;
	    apiKey: string;
	
	    static createFrom(source: any = {}) {
	        return new SettingsInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.selectedModel = source["selectedModel"];
	        this.temperature = source["temperature"];
	        this.maxTokens = source["maxTokens"];
	        this.customApiEndpoint = source["customApiEndpoint"];
	        this.customModel = source["customModel"];
	        this.promptImprove = source["promptImprove"];
	        this.actionPrompts = source["actionPrompts"];
	        this.useConversationHistory = source["useConversationHistory"];
	        this.apiKey = source["apiKey"];
	    }
	}
	export class Source {
	    id: string;
	    type: string;
	    name: string;
	    url: string;
	    username: string;
	    syncIntervalMin: number;
	    lastSyncedAt: number;
	    lastError?: string;
	    lastErrorAt?: number;
	    accountId?: string;
	    enabled: boolean;
	    writable: boolean;
	    createdAt: number;
	    itipMode?: string;
	    organizerIdentities: string[];
	
	    static createFrom(source: any = {}) {
	        return new Source(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.type = source["type"];
	        this.name = source["name"];
	        this.url = source["url"];
	        this.username = source["username"];
	        this.syncIntervalMin = source["syncIntervalMin"];
	        this.lastSyncedAt = source["lastSyncedAt"];
	        this.lastError = source["lastError"];
	        this.lastErrorAt = source["lastErrorAt"];
	        this.accountId = source["accountId"];
	        this.enabled = source["enabled"];
	        this.writable = source["writable"];
	        this.createdAt = source["createdAt"];
	        this.itipMode = source["itipMode"];
	        this.organizerIdentities = source["organizerIdentities"];
	    }
	}
	export class Status {
	    enabled: boolean;
	    hasApiKey: boolean;
	    selectedModel: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.hasApiKey = source["hasApiKey"];
	        this.selectedModel = source["selectedModel"];
	    }
	}

}

export namespace carddav {
	
	export class Addressbook {
	    id: string;
	    source_id: string;
	    path: string;
	    name: string;
	    enabled: boolean;
	    sync_token?: string;
	    // Go type: time
	    last_synced_at?: any;
	
	    static createFrom(source: any = {}) {
	        return new Addressbook(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.source_id = source["source_id"];
	        this.path = source["path"];
	        this.name = source["name"];
	        this.enabled = source["enabled"];
	        this.sync_token = source["sync_token"];
	        this.last_synced_at = this.convertValues(source["last_synced_at"], null);
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
	export class AddressbookInfo {
	    path: string;
	    name: string;
	    description?: string;
	
	    static createFrom(source: any = {}) {
	        return new AddressbookInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.description = source["description"];
	    }
	}
	export class Source {
	    id: string;
	    name: string;
	    type: string;
	    url: string;
	    username: string;
	    account_id?: string;
	    enabled: boolean;
	    writable: boolean;
	    sync_interval: number;
	    // Go type: time
	    last_synced_at?: any;
	    last_error?: string;
	    // Go type: time
	    last_error_at?: any;
	    // Go type: time
	    created_at: any;
	    addressbooks?: Addressbook[];
	
	    static createFrom(source: any = {}) {
	        return new Source(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.type = source["type"];
	        this.url = source["url"];
	        this.username = source["username"];
	        this.account_id = source["account_id"];
	        this.enabled = source["enabled"];
	        this.writable = source["writable"];
	        this.sync_interval = source["sync_interval"];
	        this.last_synced_at = this.convertValues(source["last_synced_at"], null);
	        this.last_error = source["last_error"];
	        this.last_error_at = this.convertValues(source["last_error_at"], null);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.addressbooks = this.convertValues(source["addressbooks"], Addressbook);
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
	export class SourceConfig {
	    name: string;
	    type: string;
	    url: string;
	    username: string;
	    password: string;
	    account_id?: string;
	    enabled: boolean;
	    writable: boolean;
	    sync_interval: number;
	    enabled_addressbooks?: string[];
	    addressbook_names?: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new SourceConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.type = source["type"];
	        this.url = source["url"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.account_id = source["account_id"];
	        this.enabled = source["enabled"];
	        this.writable = source["writable"];
	        this.sync_interval = source["sync_interval"];
	        this.enabled_addressbooks = source["enabled_addressbooks"];
	        this.addressbook_names = source["addressbook_names"];
	    }
	}
	export class SourceError {
	    source_id: string;
	    source_name: string;
	    error: string;
	    // Go type: time
	    error_at: any;
	
	    static createFrom(source: any = {}) {
	        return new SourceError(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source_id = source["source_id"];
	        this.source_name = source["source_name"];
	        this.error = source["error"];
	        this.error_at = this.convertValues(source["error_at"], null);
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

export namespace certificate {
	
	export class CertificateInfo {
	    subject: string;
	    issuer: string;
	    fingerprint: string;
	    notBefore: string;
	    notAfter: string;
	    dnsNames: string[];
	    isExpired: boolean;
	    errorReason: string;
	
	    static createFrom(source: any = {}) {
	        return new CertificateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.subject = source["subject"];
	        this.issuer = source["issuer"];
	        this.fingerprint = source["fingerprint"];
	        this.notBefore = source["notBefore"];
	        this.notAfter = source["notAfter"];
	        this.dnsNames = source["dnsNames"];
	        this.isExpired = source["isExpired"];
	        this.errorReason = source["errorReason"];
	    }
	}

}

export namespace contact {
	
	export class Contact {
	    email: string;
	    display_name: string;
	    source: string;
	    kind?: string;
	    avatar_url?: string;
	    send_count: number;
	    // Go type: time
	    last_used: any;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new Contact(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.display_name = source["display_name"];
	        this.source = source["source"];
	        this.kind = source["kind"];
	        this.avatar_url = source["avatar_url"];
	        this.send_count = source["send_count"];
	        this.last_used = this.convertValues(source["last_used"], null);
	        this.created_at = this.convertValues(source["created_at"], null);
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
	export class ContactPhoto {
	    email: string;
	    data: string;
	    mediaType: string;
	
	    static createFrom(source: any = {}) {
	        return new ContactPhoto(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.data = source["data"];
	        this.mediaType = source["mediaType"];
	    }
	}

}

export namespace draft {
	
	export class Draft {
	    id: string;
	    accountId: string;
	    toList: string;
	    ccList: string;
	    bccList: string;
	    replyTo?: string;
	    subject: string;
	    bodyHtml: string;
	    bodyText: string;
	    inReplyToId?: string;
	    replyType?: string;
	    referencesList?: string;
	    identityId?: string;
	    signMessage?: boolean;
	    encrypted?: boolean;
	    pgpSignMessage?: boolean;
	    pgpEncrypted?: boolean;
	    syncStatus: string;
	    imapUid?: number;
	    folderId?: string;
	    // Go type: time
	    lastSyncAttempt?: any;
	    syncError?: string;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    updatedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Draft(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.accountId = source["accountId"];
	        this.toList = source["toList"];
	        this.ccList = source["ccList"];
	        this.bccList = source["bccList"];
	        this.replyTo = source["replyTo"];
	        this.subject = source["subject"];
	        this.bodyHtml = source["bodyHtml"];
	        this.bodyText = source["bodyText"];
	        this.inReplyToId = source["inReplyToId"];
	        this.replyType = source["replyType"];
	        this.referencesList = source["referencesList"];
	        this.identityId = source["identityId"];
	        this.signMessage = source["signMessage"];
	        this.encrypted = source["encrypted"];
	        this.pgpSignMessage = source["pgpSignMessage"];
	        this.pgpEncrypted = source["pgpEncrypted"];
	        this.syncStatus = source["syncStatus"];
	        this.imapUid = source["imapUid"];
	        this.folderId = source["folderId"];
	        this.lastSyncAttempt = this.convertValues(source["lastSyncAttempt"], null);
	        this.syncError = source["syncError"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
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

export namespace folder {
	
	export class Folder {
	    id: string;
	    accountId: string;
	    name: string;
	    path: string;
	    type: string;
	    parentId?: string;
	    uidValidity: number;
	    uidNext: number;
	    highestModSeq: number;
	    totalCount: number;
	    unreadCount: number;
	    // Go type: time
	    lastSync?: any;
	    subscribed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Folder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.accountId = source["accountId"];
	        this.name = source["name"];
	        this.path = source["path"];
	        this.type = source["type"];
	        this.parentId = source["parentId"];
	        this.uidValidity = source["uidValidity"];
	        this.uidNext = source["uidNext"];
	        this.highestModSeq = source["highestModSeq"];
	        this.totalCount = source["totalCount"];
	        this.unreadCount = source["unreadCount"];
	        this.lastSync = this.convertValues(source["lastSync"], null);
	        this.subscribed = source["subscribed"];
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
	export class FolderTree {
	    folder?: Folder;
	    children?: FolderTree[];
	
	    static createFrom(source: any = {}) {
	        return new FolderTree(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folder = this.convertValues(source["folder"], Folder);
	        this.children = this.convertValues(source["children"], FolderTree);
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

export namespace imap {
	
	export class Client {
	
	
	    static createFrom(source: any = {}) {
	        return new Client(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	
	    }
	}

}

export namespace mailmerge {
	
	export class GlobalAttachment {
	    filename: string;
	    contentType: string;
	    size: number;
	    dataBase64: string;
	
	    static createFrom(source: any = {}) {
	        return new GlobalAttachment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.contentType = source["contentType"];
	        this.size = source["size"];
	        this.dataBase64 = source["dataBase64"];
	    }
	}
	export class InvalidEmail {
	    row: number;
	    address: string;
	
	    static createFrom(source: any = {}) {
	        return new InvalidEmail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.row = source["row"];
	        this.address = source["address"];
	    }
	}
	export class MissingAttachment {
	    row: number;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new MissingAttachment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.row = source["row"];
	        this.path = source["path"];
	    }
	}
	export class ParsedFile {
	    columns: string[];
	    rowCount: number;
	    previewRows: any[];
	    sheets?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ParsedFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.columns = source["columns"];
	        this.rowCount = source["rowCount"];
	        this.previewRows = source["previewRows"];
	        this.sheets = source["sheets"];
	    }
	}
	export class ParsedFileFull {
	    columns: string[];
	    rows: any[];
	    sheets?: string[];
	    rowCount: number;
	
	    static createFrom(source: any = {}) {
	        return new ParsedFileFull(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.columns = source["columns"];
	        this.rows = source["rows"];
	        this.sheets = source["sheets"];
	        this.rowCount = source["rowCount"];
	    }
	}
	export class RenderedMessage {
	    rowIndex: number;
	    to: string;
	    cc: string;
	    bcc: string;
	    subject: string;
	    bodyHtml: string;
	    bodyText: string;
	    attachments: string[];
	
	    static createFrom(source: any = {}) {
	        return new RenderedMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rowIndex = source["rowIndex"];
	        this.to = source["to"];
	        this.cc = source["cc"];
	        this.bcc = source["bcc"];
	        this.subject = source["subject"];
	        this.bodyHtml = source["bodyHtml"];
	        this.bodyText = source["bodyText"];
	        this.attachments = source["attachments"];
	    }
	}
	export class RowResult {
	    index: number;
	    email: string;
	    status: string;
	    error?: string;
	    messageId?: string;
	    // Go type: time
	    timestamp: any;
	
	    static createFrom(source: any = {}) {
	        return new RowResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.email = source["email"];
	        this.status = source["status"];
	        this.error = source["error"];
	        this.messageId = source["messageId"];
	        this.timestamp = this.convertValues(source["timestamp"], null);
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
	export class Run {
	    id: string;
	    // Go type: time
	    createdAt: any;
	    accountId: string;
	    deliverMode: string;
	    status: string;
	    totalRows: number;
	    sentCount: number;
	    failedCount: number;
	    skippedCount: number;
	    heldCount: number;
	    queuedCount: number;
	    rows: RowResult[];
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new Run(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.accountId = source["accountId"];
	        this.deliverMode = source["deliverMode"];
	        this.status = source["status"];
	        this.totalRows = source["totalRows"];
	        this.sentCount = source["sentCount"];
	        this.failedCount = source["failedCount"];
	        this.skippedCount = source["skippedCount"];
	        this.heldCount = source["heldCount"];
	        this.queuedCount = source["queuedCount"];
	        this.rows = this.convertValues(source["rows"], RowResult);
	        this.error = source["error"];
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
	export class Template {
	    accountId: string;
	    from: string;
	    to: string;
	    cc: string;
	    bcc: string;
	    replyTo: string;
	    subject: string;
	    bodyHtml: string;
	    bodyText: string;
	    isPlainText: boolean;
	    sign: boolean;
	    signKind?: string;
	    globalAttachments: GlobalAttachment[];
	    attachmentField: string;
	
	    static createFrom(source: any = {}) {
	        return new Template(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accountId = source["accountId"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.cc = source["cc"];
	        this.bcc = source["bcc"];
	        this.replyTo = source["replyTo"];
	        this.subject = source["subject"];
	        this.bodyHtml = source["bodyHtml"];
	        this.bodyText = source["bodyText"];
	        this.isPlainText = source["isPlainText"];
	        this.sign = source["sign"];
	        this.signKind = source["signKind"];
	        this.globalAttachments = this.convertValues(source["globalAttachments"], GlobalAttachment);
	        this.attachmentField = source["attachmentField"];
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
	export class RunRequest {
	    template: Template;
	    rows: any[];
	    messageIds?: string[];
	    sourceKind: string;
	    deliverMode: string;
	    pauseSeconds: number;
	    randomPause: boolean;
	    startRow: number;
	    stopRow: number;
	    capEnabled: boolean;
	    capLimit: number;
	    accountId: string;
	    attachmentDir: string;
	
	    static createFrom(source: any = {}) {
	        return new RunRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.template = this.convertValues(source["template"], Template);
	        this.rows = source["rows"];
	        this.messageIds = source["messageIds"];
	        this.sourceKind = source["sourceKind"];
	        this.deliverMode = source["deliverMode"];
	        this.pauseSeconds = source["pauseSeconds"];
	        this.randomPause = source["randomPause"];
	        this.startRow = source["startRow"];
	        this.stopRow = source["stopRow"];
	        this.capEnabled = source["capEnabled"];
	        this.capLimit = source["capLimit"];
	        this.accountId = source["accountId"];
	        this.attachmentDir = source["attachmentDir"];
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
	export class RunSummary {
	    id: string;
	    // Go type: time
	    createdAt: any;
	    accountId: string;
	    deliverMode: string;
	    status: string;
	    totalRows: number;
	    sentCount: number;
	    failedCount: number;
	    skippedCount: number;
	    heldCount: number;
	    queuedCount: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new RunSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.accountId = source["accountId"];
	        this.deliverMode = source["deliverMode"];
	        this.status = source["status"];
	        this.totalRows = source["totalRows"];
	        this.sentCount = source["sentCount"];
	        this.failedCount = source["failedCount"];
	        this.skippedCount = source["skippedCount"];
	        this.heldCount = source["heldCount"];
	        this.queuedCount = source["queuedCount"];
	        this.error = source["error"];
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
	export class SigningInfo {
	    available: boolean;
	    kind: string;
	    defaultSign: boolean;
	    reason?: string;
	
	    static createFrom(source: any = {}) {
	        return new SigningInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.kind = source["kind"];
	        this.defaultSign = source["defaultSign"];
	        this.reason = source["reason"];
	    }
	}
	
	export class ValidationResult {
	    unresolvedVariables: string[];
	    invalidEmails: InvalidEmail[];
	    duplicateEmails: number;
	    emptyToRows: number[];
	    missingAttachmentFiles: MissingAttachment[];
	    signingError?: string;
	
	    static createFrom(source: any = {}) {
	        return new ValidationResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.unresolvedVariables = source["unresolvedVariables"];
	        this.invalidEmails = this.convertValues(source["invalidEmails"], InvalidEmail);
	        this.duplicateEmails = source["duplicateEmails"];
	        this.emptyToRows = source["emptyToRows"];
	        this.missingAttachmentFiles = this.convertValues(source["missingAttachmentFiles"], MissingAttachment);
	        this.signingError = source["signingError"];
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

export namespace message {
	
	export class Address {
	    name: string;
	    email: string;
	
	    static createFrom(source: any = {}) {
	        return new Address(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.email = source["email"];
	    }
	}
	export class Attachment {
	    id: string;
	    messageId: string;
	    filename: string;
	    contentType: string;
	    size: number;
	    contentId?: string;
	    isInline: boolean;
	    localPath?: string;
	
	    static createFrom(source: any = {}) {
	        return new Attachment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.messageId = source["messageId"];
	        this.filename = source["filename"];
	        this.contentType = source["contentType"];
	        this.size = source["size"];
	        this.contentId = source["contentId"];
	        this.isInline = source["isInline"];
	        this.localPath = source["localPath"];
	    }
	}
	export class CategoryCounts {
	    personal: number;
	    automated: number;
	    total: number;
	    subCategories: Record<string, number>;
	
	    static createFrom(source: any = {}) {
	        return new CategoryCounts(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.personal = source["personal"];
	        this.automated = source["automated"];
	        this.total = source["total"];
	        this.subCategories = source["subCategories"];
	    }
	}
	export class Message {
	    id: string;
	    accountId: string;
	    folderId: string;
	    uid: number;
	    messageId?: string;
	    inReplyTo?: string;
	    references?: string;
	    threadId?: string;
	    subject: string;
	    fromName: string;
	    fromEmail: string;
	    toList?: string;
	    ccList?: string;
	    bccList?: string;
	    replyTo?: string;
	    // Go type: time
	    date: any;
	    snippet?: string;
	    isRead: boolean;
	    isStarred: boolean;
	    isAnswered: boolean;
	    isForwarded: boolean;
	    isDraft: boolean;
	    isDeleted: boolean;
	    size: number;
	    hasAttachments: boolean;
	    bodyText?: string;
	    bodyHtml?: string;
	    bodyFetched: boolean;
	    bodyTruncated?: boolean;
	    bodyFailed?: boolean;
	    listUnsubscribe?: string;
	    listUnsubscribePost?: boolean;
	    category?: string;
	    subCategory?: string;
	    extractKind?: string;
	    extractValue?: string;
	    extractRule?: string;
	    readReceiptTo?: string;
	    readReceiptHandled: boolean;
	    smimeStatus?: string;
	    smimeSignerEmail?: string;
	    smimeSignerSubject?: string;
	    smimeEncrypted?: boolean;
	    hasSMIME?: boolean;
	    pgpStatus?: string;
	    pgpSignerEmail?: string;
	    pgpSignerKeyId?: string;
	    pgpEncrypted?: boolean;
	    hasPGP?: boolean;
	    // Go type: time
	    receivedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Message(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.accountId = source["accountId"];
	        this.folderId = source["folderId"];
	        this.uid = source["uid"];
	        this.messageId = source["messageId"];
	        this.inReplyTo = source["inReplyTo"];
	        this.references = source["references"];
	        this.threadId = source["threadId"];
	        this.subject = source["subject"];
	        this.fromName = source["fromName"];
	        this.fromEmail = source["fromEmail"];
	        this.toList = source["toList"];
	        this.ccList = source["ccList"];
	        this.bccList = source["bccList"];
	        this.replyTo = source["replyTo"];
	        this.date = this.convertValues(source["date"], null);
	        this.snippet = source["snippet"];
	        this.isRead = source["isRead"];
	        this.isStarred = source["isStarred"];
	        this.isAnswered = source["isAnswered"];
	        this.isForwarded = source["isForwarded"];
	        this.isDraft = source["isDraft"];
	        this.isDeleted = source["isDeleted"];
	        this.size = source["size"];
	        this.hasAttachments = source["hasAttachments"];
	        this.bodyText = source["bodyText"];
	        this.bodyHtml = source["bodyHtml"];
	        this.bodyFetched = source["bodyFetched"];
	        this.bodyTruncated = source["bodyTruncated"];
	        this.bodyFailed = source["bodyFailed"];
	        this.listUnsubscribe = source["listUnsubscribe"];
	        this.listUnsubscribePost = source["listUnsubscribePost"];
	        this.category = source["category"];
	        this.subCategory = source["subCategory"];
	        this.extractKind = source["extractKind"];
	        this.extractValue = source["extractValue"];
	        this.extractRule = source["extractRule"];
	        this.readReceiptTo = source["readReceiptTo"];
	        this.readReceiptHandled = source["readReceiptHandled"];
	        this.smimeStatus = source["smimeStatus"];
	        this.smimeSignerEmail = source["smimeSignerEmail"];
	        this.smimeSignerSubject = source["smimeSignerSubject"];
	        this.smimeEncrypted = source["smimeEncrypted"];
	        this.hasSMIME = source["hasSMIME"];
	        this.pgpStatus = source["pgpStatus"];
	        this.pgpSignerEmail = source["pgpSignerEmail"];
	        this.pgpSignerKeyId = source["pgpSignerKeyId"];
	        this.pgpEncrypted = source["pgpEncrypted"];
	        this.hasPGP = source["hasPGP"];
	        this.receivedAt = this.convertValues(source["receivedAt"], null);
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
	export class Conversation {
	    threadId: string;
	    subject: string;
	    snippet: string;
	    messageCount: number;
	    unreadCount: number;
	    hasAttachments: boolean;
	    isStarred: boolean;
	    // Go type: time
	    latestDate: any;
	    participants: Address[];
	    messageIds: string[];
	    isEncrypted: boolean;
	    messages?: Message[];
	    extractKind?: string;
	    extractValue?: string;
	    extractRule?: string;
	    extractMessageId?: string;
	    category?: string;
	    subCategory?: string;
	    accountId?: string;
	    accountName?: string;
	    accountColor?: string;
	    folderId?: string;
	
	    static createFrom(source: any = {}) {
	        return new Conversation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.threadId = source["threadId"];
	        this.subject = source["subject"];
	        this.snippet = source["snippet"];
	        this.messageCount = source["messageCount"];
	        this.unreadCount = source["unreadCount"];
	        this.hasAttachments = source["hasAttachments"];
	        this.isStarred = source["isStarred"];
	        this.latestDate = this.convertValues(source["latestDate"], null);
	        this.participants = this.convertValues(source["participants"], Address);
	        this.messageIds = source["messageIds"];
	        this.isEncrypted = source["isEncrypted"];
	        this.messages = this.convertValues(source["messages"], Message);
	        this.extractKind = source["extractKind"];
	        this.extractValue = source["extractValue"];
	        this.extractRule = source["extractRule"];
	        this.extractMessageId = source["extractMessageId"];
	        this.category = source["category"];
	        this.subCategory = source["subCategory"];
	        this.accountId = source["accountId"];
	        this.accountName = source["accountName"];
	        this.accountColor = source["accountColor"];
	        this.folderId = source["folderId"];
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
	export class ConversationSearchResult {
	    threadId: string;
	    subject: string;
	    snippet: string;
	    messageCount: number;
	    unreadCount: number;
	    hasAttachments: boolean;
	    isStarred: boolean;
	    // Go type: time
	    latestDate: any;
	    participants: Address[];
	    messageIds: string[];
	    isEncrypted: boolean;
	    messages?: Message[];
	    extractKind?: string;
	    extractValue?: string;
	    extractRule?: string;
	    extractMessageId?: string;
	    category?: string;
	    subCategory?: string;
	    accountId?: string;
	    accountName?: string;
	    accountColor?: string;
	    folderId?: string;
	    highlightedSubject: string;
	    highlightedSnippet: string;
	    highlightedFromName: string;
	    folderName: string;
	    folderType: string;
	
	    static createFrom(source: any = {}) {
	        return new ConversationSearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.threadId = source["threadId"];
	        this.subject = source["subject"];
	        this.snippet = source["snippet"];
	        this.messageCount = source["messageCount"];
	        this.unreadCount = source["unreadCount"];
	        this.hasAttachments = source["hasAttachments"];
	        this.isStarred = source["isStarred"];
	        this.latestDate = this.convertValues(source["latestDate"], null);
	        this.participants = this.convertValues(source["participants"], Address);
	        this.messageIds = source["messageIds"];
	        this.isEncrypted = source["isEncrypted"];
	        this.messages = this.convertValues(source["messages"], Message);
	        this.extractKind = source["extractKind"];
	        this.extractValue = source["extractValue"];
	        this.extractRule = source["extractRule"];
	        this.extractMessageId = source["extractMessageId"];
	        this.category = source["category"];
	        this.subCategory = source["subCategory"];
	        this.accountId = source["accountId"];
	        this.accountName = source["accountName"];
	        this.accountColor = source["accountColor"];
	        this.folderId = source["folderId"];
	        this.highlightedSubject = source["highlightedSubject"];
	        this.highlightedSnippet = source["highlightedSnippet"];
	        this.highlightedFromName = source["highlightedFromName"];
	        this.folderName = source["folderName"];
	        this.folderType = source["folderType"];
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
	export class FTSIndexStatus {
	    folderId: string;
	    indexedCount: number;
	    totalCount: number;
	    isComplete: boolean;
	    lastIndexedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new FTSIndexStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folderId = source["folderId"];
	        this.indexedCount = source["indexedCount"];
	        this.totalCount = source["totalCount"];
	        this.isComplete = source["isComplete"];
	        this.lastIndexedAt = source["lastIndexedAt"];
	    }
	}
	
	export class MessageHeader {
	    id: string;
	    accountId: string;
	    folderId: string;
	    uid: number;
	    subject: string;
	    fromName: string;
	    fromEmail: string;
	    // Go type: time
	    date: any;
	    snippet: string;
	    isRead: boolean;
	    isStarred: boolean;
	    hasAttachments: boolean;
	    extractKind?: string;
	    extractValue?: string;
	    extractRule?: string;
	
	    static createFrom(source: any = {}) {
	        return new MessageHeader(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.accountId = source["accountId"];
	        this.folderId = source["folderId"];
	        this.uid = source["uid"];
	        this.subject = source["subject"];
	        this.fromName = source["fromName"];
	        this.fromEmail = source["fromEmail"];
	        this.date = this.convertValues(source["date"], null);
	        this.snippet = source["snippet"];
	        this.isRead = source["isRead"];
	        this.isStarred = source["isStarred"];
	        this.hasAttachments = source["hasAttachments"];
	        this.extractKind = source["extractKind"];
	        this.extractValue = source["extractValue"];
	        this.extractRule = source["extractRule"];
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

export namespace pgp {
	
	export class Key {
	    id: string;
	    accountId: string;
	    email: string;
	    keyId: string;
	    fingerprint: string;
	    userId: string;
	    algorithm: string;
	    keySize: number;
	    // Go type: time
	    createdAtKey?: any;
	    // Go type: time
	    expiresAtKey?: any;
	    isDefault: boolean;
	    isExpired: boolean;
	    hasPrivate: boolean;
	    // Go type: time
	    createdAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Key(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.accountId = source["accountId"];
	        this.email = source["email"];
	        this.keyId = source["keyId"];
	        this.fingerprint = source["fingerprint"];
	        this.userId = source["userId"];
	        this.algorithm = source["algorithm"];
	        this.keySize = source["keySize"];
	        this.createdAtKey = this.convertValues(source["createdAtKey"], null);
	        this.expiresAtKey = this.convertValues(source["expiresAtKey"], null);
	        this.isDefault = source["isDefault"];
	        this.isExpired = source["isExpired"];
	        this.hasPrivate = source["hasPrivate"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
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
	export class ImportResult {
	    key?: Key;
	    hasPrivate: boolean;
	    subkeyCount: number;
	
	    static createFrom(source: any = {}) {
	        return new ImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = this.convertValues(source["key"], Key);
	        this.hasPrivate = source["hasPrivate"];
	        this.subkeyCount = source["subkeyCount"];
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
	
	export class KeyServer {
	    id: number;
	    url: string;
	    orderIndex: number;
	
	    static createFrom(source: any = {}) {
	        return new KeyServer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.url = source["url"];
	        this.orderIndex = source["orderIndex"];
	    }
	}
	export class SenderKey {
	    id: string;
	    email: string;
	    keyId: string;
	    fingerprint: string;
	    userId: string;
	    algorithm: string;
	    keySize: number;
	    // Go type: time
	    createdAtKey?: any;
	    // Go type: time
	    expiresAtKey?: any;
	    source: string;
	    // Go type: time
	    collectedAt: any;
	    // Go type: time
	    lastSeenAt: any;
	
	    static createFrom(source: any = {}) {
	        return new SenderKey(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.email = source["email"];
	        this.keyId = source["keyId"];
	        this.fingerprint = source["fingerprint"];
	        this.userId = source["userId"];
	        this.algorithm = source["algorithm"];
	        this.keySize = source["keySize"];
	        this.createdAtKey = this.convertValues(source["createdAtKey"], null);
	        this.expiresAtKey = this.convertValues(source["expiresAtKey"], null);
	        this.source = source["source"];
	        this.collectedAt = this.convertValues(source["collectedAt"], null);
	        this.lastSeenAt = this.convertValues(source["lastSeenAt"], null);
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

export namespace platform {
	
	export class WindowControlsLayout {
	    side: string;
	    order: string[];
	    style: string;
	
	    static createFrom(source: any = {}) {
	        return new WindowControlsLayout(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.side = source["side"];
	        this.order = source["order"];
	        this.style = source["style"];
	    }
	}

}

export namespace sendqueue {
	
	export class Item {
	    id: string;
	    accountId: string;
	    draftId: string;
	    kind: string;
	    reactionMessageId?: string;
	    subject: string;
	    recipient: string;
	    fromAddress: string;
	    to: string[];
	    status: string;
	    attempts: number;
	    maxAttempts: number;
	    // Go type: time
	    sendAt: any;
	    // Go type: time
	    dueAt: any;
	    lastError?: string;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    sentAt?: any;
	
	    static createFrom(source: any = {}) {
	        return new Item(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.accountId = source["accountId"];
	        this.draftId = source["draftId"];
	        this.kind = source["kind"];
	        this.reactionMessageId = source["reactionMessageId"];
	        this.subject = source["subject"];
	        this.recipient = source["recipient"];
	        this.fromAddress = source["fromAddress"];
	        this.to = source["to"];
	        this.status = source["status"];
	        this.attempts = source["attempts"];
	        this.maxAttempts = source["maxAttempts"];
	        this.sendAt = this.convertValues(source["sendAt"], null);
	        this.dueAt = this.convertValues(source["dueAt"], null);
	        this.lastError = source["lastError"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.sentAt = this.convertValues(source["sentAt"], null);
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
	export class Stats {
	    total: number;
	    pending: number;
	    sent: number;
	    failed: number;
	    cancelled: number;
	    sentUnsaved: number;
	
	    static createFrom(source: any = {}) {
	        return new Stats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.pending = source["pending"];
	        this.sent = source["sent"];
	        this.failed = source["failed"];
	        this.cancelled = source["cancelled"];
	        this.sentUnsaved = source["sentUnsaved"];
	    }
	}

}

export namespace settings {
	
	export class AllowlistEntry {
	    id: number;
	    type: string;
	    value: string;
	    createdAt: string;
	
	    static createFrom(source: any = {}) {
	        return new AllowlistEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.type = source["type"];
	        this.value = source["value"];
	        this.createdAt = source["createdAt"];
	    }
	}

}

export namespace smime {
	
	export class Certificate {
	    id: string;
	    accountId: string;
	    email: string;
	    subject: string;
	    issuer: string;
	    serialNumber: string;
	    fingerprint: string;
	    // Go type: time
	    notBefore: any;
	    // Go type: time
	    notAfter: any;
	    isDefault: boolean;
	    isExpired: boolean;
	    isSelfSigned: boolean;
	    // Go type: time
	    createdAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Certificate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.accountId = source["accountId"];
	        this.email = source["email"];
	        this.subject = source["subject"];
	        this.issuer = source["issuer"];
	        this.serialNumber = source["serialNumber"];
	        this.fingerprint = source["fingerprint"];
	        this.notBefore = this.convertValues(source["notBefore"], null);
	        this.notAfter = this.convertValues(source["notAfter"], null);
	        this.isDefault = source["isDefault"];
	        this.isExpired = source["isExpired"];
	        this.isSelfSigned = source["isSelfSigned"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
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
	export class ImportResult {
	    certificate?: Certificate;
	    chainLength: number;
	
	    static createFrom(source: any = {}) {
	        return new ImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.certificate = this.convertValues(source["certificate"], Certificate);
	        this.chainLength = source["chainLength"];
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
	export class SenderCert {
	    id: string;
	    email: string;
	    subject: string;
	    issuer: string;
	    serialNumber: string;
	    fingerprint: string;
	    // Go type: time
	    notBefore: any;
	    // Go type: time
	    notAfter: any;
	    // Go type: time
	    collectedAt: any;
	    // Go type: time
	    lastSeenAt: any;
	
	    static createFrom(source: any = {}) {
	        return new SenderCert(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.email = source["email"];
	        this.subject = source["subject"];
	        this.issuer = source["issuer"];
	        this.serialNumber = source["serialNumber"];
	        this.fingerprint = source["fingerprint"];
	        this.notBefore = this.convertValues(source["notBefore"], null);
	        this.notAfter = this.convertValues(source["notAfter"], null);
	        this.collectedAt = this.convertValues(source["collectedAt"], null);
	        this.lastSeenAt = this.convertValues(source["lastSeenAt"], null);
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

export namespace smtp {
	
	export class Address {
	    name: string;
	    address: string;
	
	    static createFrom(source: any = {}) {
	        return new Address(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.address = source["address"];
	    }
	}
	export class Attachment {
	    filename: string;
	    content_type: string;
	    content: number[];
	    content_base64?: string;
	    content_id: string;
	    inline: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Attachment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.content_type = source["content_type"];
	        this.content = source["content"];
	        this.content_base64 = source["content_base64"];
	        this.content_id = source["content_id"];
	        this.inline = source["inline"];
	    }
	}
	export class ComposeMessage {
	    from: Address;
	    to: Address[];
	    cc: Address[];
	    bcc: Address[];
	    reply_to?: Address;
	    subject: string;
	    text_body: string;
	    html_body: string;
	    attachments: Attachment[];
	    in_reply_to?: string;
	    references?: string[];
	    message_id?: string;
	    request_read_receipt: boolean;
	    sign_message: boolean;
	    encrypt_message: boolean;
	    pgp_sign_message: boolean;
	    pgp_encrypt_message: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ComposeMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from = this.convertValues(source["from"], Address);
	        this.to = this.convertValues(source["to"], Address);
	        this.cc = this.convertValues(source["cc"], Address);
	        this.bcc = this.convertValues(source["bcc"], Address);
	        this.reply_to = this.convertValues(source["reply_to"], Address);
	        this.subject = source["subject"];
	        this.text_body = source["text_body"];
	        this.html_body = source["html_body"];
	        this.attachments = this.convertValues(source["attachments"], Attachment);
	        this.in_reply_to = source["in_reply_to"];
	        this.references = source["references"];
	        this.message_id = source["message_id"];
	        this.request_read_receipt = source["request_read_receipt"];
	        this.sign_message = source["sign_message"];
	        this.encrypt_message = source["encrypt_message"];
	        this.pgp_sign_message = source["pgp_sign_message"];
	        this.pgp_encrypt_message = source["pgp_encrypt_message"];
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

export namespace sync {
	
	export class IMAPSearchResult {
	    uid: number;
	    messageId?: string;
	    isLocal: boolean;
	    subject: string;
	    fromName: string;
	    fromEmail: string;
	    // Go type: time
	    date: any;
	    snippet?: string;
	    isRead: boolean;
	    isStarred: boolean;
	    hasAttachments: boolean;
	    accountId: string;
	    folderId: string;
	    folderName?: string;
	
	    static createFrom(source: any = {}) {
	        return new IMAPSearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.uid = source["uid"];
	        this.messageId = source["messageId"];
	        this.isLocal = source["isLocal"];
	        this.subject = source["subject"];
	        this.fromName = source["fromName"];
	        this.fromEmail = source["fromEmail"];
	        this.date = this.convertValues(source["date"], null);
	        this.snippet = source["snippet"];
	        this.isRead = source["isRead"];
	        this.isStarred = source["isStarred"];
	        this.hasAttachments = source["hasAttachments"];
	        this.accountId = source["accountId"];
	        this.folderId = source["folderId"];
	        this.folderName = source["folderName"];
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
	export class IMAPSearchResponse {
	    results: IMAPSearchResult[];
	    totalCount: number;
	
	    static createFrom(source: any = {}) {
	        return new IMAPSearchResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.results = this.convertValues(source["results"], IMAPSearchResult);
	        this.totalCount = source["totalCount"];
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

export namespace translate {
	
	export class BergamotLanguage {
	    code: string;
	    name: string;
	    sizeBytes: number;
	    downloaded: boolean;
	    downloadedSize: number;
	    toEnglish: boolean;
	    fromEnglish: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BergamotLanguage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.name = source["name"];
	        this.sizeBytes = source["sizeBytes"];
	        this.downloaded = source["downloaded"];
	        this.downloadedSize = source["downloadedSize"];
	        this.toEnglish = source["toEnglish"];
	        this.fromEnglish = source["fromEnglish"];
	    }
	}
	export class CachedTranslation {
	    engine: string;
	    translatedSubject: string;
	    translatedBody: string;
	
	    static createFrom(source: any = {}) {
	        return new CachedTranslation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.engine = source["engine"];
	        this.translatedSubject = source["translatedSubject"];
	        this.translatedBody = source["translatedBody"];
	    }
	}
	export class DownloadedPair {
	    fromLang: string;
	    toLang: string;
	    version: string;
	    sizeBytes: number;
	    files: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new DownloadedPair(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fromLang = source["fromLang"];
	        this.toLang = source["toLang"];
	        this.version = source["version"];
	        this.sizeBytes = source["sizeBytes"];
	        this.files = source["files"];
	    }
	}
	export class EngineStatus {
	    available: boolean;
	    version?: string;
	    sizeBytes: number;
	    downloadedBytes: number;
	
	    static createFrom(source: any = {}) {
	        return new EngineStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.version = source["version"];
	        this.sizeBytes = source["sizeBytes"];
	        this.downloadedBytes = source["downloadedBytes"];
	    }
	}
	export class OSEngineStatus {
	    available: boolean;
	    name: string;
	    reason?: string;
	
	    static createFrom(source: any = {}) {
	        return new OSEngineStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.name = source["name"];
	        this.reason = source["reason"];
	    }
	}

}

export namespace v1 {
	
	export class AccountSetupHookRequest {
	    extensionId: string;
	    providers: string[];
	    buttonLabel: string;
	    description?: string;
	    component: string;
	
	    static createFrom(source: any = {}) {
	        return new AccountSetupHookRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.extensionId = source["extensionId"];
	        this.providers = source["providers"];
	        this.buttonLabel = source["buttonLabel"];
	        this.description = source["description"];
	        this.component = source["component"];
	    }
	}
	export class Addressbook {
	    id: string;
	    sourceId: string;
	    name: string;
	    path?: string;
	
	    static createFrom(source: any = {}) {
	        return new Addressbook(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.sourceId = source["sourceId"];
	        this.name = source["name"];
	        this.path = source["path"];
	    }
	}
	export class ComposerToolbarRequest {
	    extensionId: string;
	    label: string;
	    icon: string;
	    component: string;
	    order?: number;
	
	    static createFrom(source: any = {}) {
	        return new ComposerToolbarRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.extensionId = source["extensionId"];
	        this.label = source["label"];
	        this.icon = source["icon"];
	        this.component = source["component"];
	        this.order = source["order"];
	    }
	}
	export class ContactIMPP {
	    handle: string;
	    type?: string;
	
	    static createFrom(source: any = {}) {
	        return new ContactIMPP(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.handle = source["handle"];
	        this.type = source["type"];
	    }
	}
	export class ContactURL {
	    url: string;
	    type?: string;
	
	    static createFrom(source: any = {}) {
	        return new ContactURL(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.type = source["type"];
	    }
	}
	export class ContactAddress {
	    type?: string;
	    street?: string;
	    city?: string;
	    region?: string;
	    postcode?: string;
	    country?: string;
	
	    static createFrom(source: any = {}) {
	        return new ContactAddress(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.street = source["street"];
	        this.city = source["city"];
	        this.region = source["region"];
	        this.postcode = source["postcode"];
	        this.country = source["country"];
	    }
	}
	export class ContactPhone {
	    number: string;
	    type?: string;
	    isPrimary?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ContactPhone(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.number = source["number"];
	        this.type = source["type"];
	        this.isPrimary = source["isPrimary"];
	    }
	}
	export class ContactEmail {
	    email: string;
	    type?: string;
	    isPrimary?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ContactEmail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.type = source["type"];
	        this.isPrimary = source["isPrimary"];
	    }
	}
	export class Contact {
	    id: string;
	    name: string;
	    emails: string[];
	    emailItems?: ContactEmail[];
	    phones?: ContactPhone[];
	    addresses?: ContactAddress[];
	    urls?: ContactURL[];
	    impps?: ContactIMPP[];
	    org?: string;
	    title?: string;
	    note?: string;
	    bday?: string;
	    nickname?: string;
	    categories?: string[];
	    photoData?: string;
	    photoMediaType?: string;
	    photoUrl?: string;
	    sourceId?: string;
	    // Go type: time
	    updatedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Contact(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.emails = source["emails"];
	        this.emailItems = this.convertValues(source["emailItems"], ContactEmail);
	        this.phones = this.convertValues(source["phones"], ContactPhone);
	        this.addresses = this.convertValues(source["addresses"], ContactAddress);
	        this.urls = this.convertValues(source["urls"], ContactURL);
	        this.impps = this.convertValues(source["impps"], ContactIMPP);
	        this.org = source["org"];
	        this.title = source["title"];
	        this.note = source["note"];
	        this.bday = source["bday"];
	        this.nickname = source["nickname"];
	        this.categories = source["categories"];
	        this.photoData = source["photoData"];
	        this.photoMediaType = source["photoMediaType"];
	        this.photoUrl = source["photoUrl"];
	        this.sourceId = source["sourceId"];
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
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
	
	export class ContactPhoto {
	    data?: string;
	    mediaType?: string;
	    url?: string;
	
	    static createFrom(source: any = {}) {
	        return new ContactPhoto(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.data = source["data"];
	        this.mediaType = source["mediaType"];
	        this.url = source["url"];
	    }
	}
	export class ContactCreateInput {
	    sourceId?: string;
	    addressbookId?: string;
	    email: string;
	    name?: string;
	    nickname?: string;
	    org?: string;
	    title?: string;
	    note?: string;
	    bday?: string;
	    categories?: string[];
	    emails?: ContactEmail[];
	    phones?: ContactPhone[];
	    addresses?: ContactAddress[];
	    urls?: ContactURL[];
	    impps?: ContactIMPP[];
	    photo?: ContactPhoto;
	
	    static createFrom(source: any = {}) {
	        return new ContactCreateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sourceId = source["sourceId"];
	        this.addressbookId = source["addressbookId"];
	        this.email = source["email"];
	        this.name = source["name"];
	        this.nickname = source["nickname"];
	        this.org = source["org"];
	        this.title = source["title"];
	        this.note = source["note"];
	        this.bday = source["bday"];
	        this.categories = source["categories"];
	        this.emails = this.convertValues(source["emails"], ContactEmail);
	        this.phones = this.convertValues(source["phones"], ContactPhone);
	        this.addresses = this.convertValues(source["addresses"], ContactAddress);
	        this.urls = this.convertValues(source["urls"], ContactURL);
	        this.impps = this.convertValues(source["impps"], ContactIMPP);
	        this.photo = this.convertValues(source["photo"], ContactPhoto);
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
	
	
	export class ContactPatch {
	    name?: string;
	    nickname?: string;
	    org?: string;
	    title?: string;
	    note?: string;
	    bday?: string;
	    emails?: ContactEmail[];
	    phones?: ContactPhone[];
	    addresses?: ContactAddress[];
	    urls?: ContactURL[];
	    impps?: ContactIMPP[];
	    categories?: string[];
	    photo?: ContactPhoto;
	
	    static createFrom(source: any = {}) {
	        return new ContactPatch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.nickname = source["nickname"];
	        this.org = source["org"];
	        this.title = source["title"];
	        this.note = source["note"];
	        this.bday = source["bday"];
	        this.emails = this.convertValues(source["emails"], ContactEmail);
	        this.phones = this.convertValues(source["phones"], ContactPhone);
	        this.addresses = this.convertValues(source["addresses"], ContactAddress);
	        this.urls = this.convertValues(source["urls"], ContactURL);
	        this.impps = this.convertValues(source["impps"], ContactIMPP);
	        this.categories = source["categories"];
	        this.photo = this.convertValues(source["photo"], ContactPhoto);
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
	
	
	export class ContactSource {
	    id: string;
	    name: string;
	    type: string;
	    writable: boolean;
	    accountId?: string;
	
	    static createFrom(source: any = {}) {
	        return new ContactSource(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.type = source["type"];
	        this.writable = source["writable"];
	        this.accountId = source["accountId"];
	    }
	}
	
	export class RailTabRequest {
	    extensionId: string;
	    label: string;
	    icon: string;
	    component: string;
	    order?: number;
	
	    static createFrom(source: any = {}) {
	        return new RailTabRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.extensionId = source["extensionId"];
	        this.label = source["label"];
	        this.icon = source["icon"];
	        this.component = source["component"];
	        this.order = source["order"];
	    }
	}

}

export namespace time {

	export class Time {


	    static createFrom(source: any = {}) {
	        return new Time(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);

	    }
	}

}
